package main

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"image-api/internal/jobdb"

	"cloud.google.com/go/pubsub"
	_ "github.com/go-sql-driver/mysql"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	dbDSN := os.Getenv("JOB_DB_DSN")
	if dbDSN == "" {
		fatal("JOB_DB_DSN is required")
	}
	projectID := os.Getenv("GCP_PROJECT_ID")
	if projectID == "" {
		fatal("GCP_PROJECT_ID is required")
	}
	topicName := os.Getenv("PUBSUB_TOPIC")
	if topicName == "" {
		fatal("PUBSUB_TOPIC is required")
	}
	pubsubMode := os.Getenv("PUBSUB_MODE")
	if pubsubMode == "" {
		pubsubMode = "cloud"
	}
	subscriptionName := os.Getenv("PUBSUB_SUBSCRIPTION")
	if subscriptionName == "" {
		subscriptionName = "image-jobs-push"
	}
	pushEndpoint := os.Getenv("PUBSUB_PUSH_ENDPOINT")
	if pushEndpoint == "" {
		pushEndpoint = "http://worker:8080/pubsub/jobs"
	}

	pollInterval := 2 * time.Second
	if raw := os.Getenv("OUTBOX_POLL_INTERVAL"); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			pollInterval = time.Duration(v * float64(time.Second))
		}
	}
	batchSize := 10
	if raw := os.Getenv("OUTBOX_BATCH_SIZE"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			batchSize = v
		}
	}

	db, err := jobdb.Open(dbDSN)
	if err != nil {
		fatal("failed to open job db", "err", err)
	}
	defer db.Close()

	pubsubClient, err := pubsub.NewClient(context.Background(), projectID)
	if err != nil {
		fatal("failed to create pubsub client", "err", err)
	}
	defer pubsubClient.Close()

	publisher := pubsubClient.Topic(topicName)
	defer publisher.Stop()

	if pubsubMode == "emulator" {
		if err := ensureTopicWithRetry(context.Background(), pubsubClient, topicName, 10, 500*time.Millisecond); err != nil {
			fatal("failed to ensure pubsub topic", "err", err)
		}
		if err := ensureSubscription(context.Background(), pubsubClient, topicName, subscriptionName, pushEndpoint); err != nil {
			fatal("failed to ensure pubsub subscription", "err", err)
		}
	}

	ctx := context.Background()
	go runPublisherLoop(ctx, db, publisher, pollInterval, batchSize)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		// Health check endpoint for Cloud Run readiness.
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		checkCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.PingContext(checkCtx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("not ready"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	slog.Info("publisher listening", "addr", ":"+port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		fatal("publisher server failed", "err", err)
	}
}

func ensureTopic(ctx context.Context, client *pubsub.Client, topicName string) error {
	topic := client.Topic(topicName)
	exists, err := topic.Exists(ctx)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = client.CreateTopic(ctx, topicName)
	if status.Code(err) == codes.AlreadyExists {
		return nil
	}
	return err
}

func ensureTopicWithRetry(ctx context.Context, client *pubsub.Client, topicName string, attempts int, delay time.Duration) error {
	var lastErr error
	for i := 0; i < attempts; i++ {
		if err := ensureTopic(ctx, client, topicName); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(delay)
	}
	return lastErr
}

func ensureSubscription(ctx context.Context, client *pubsub.Client, topicName, subName, pushEndpoint string) error {
	sub := client.Subscription(subName)
	exists, err := sub.Exists(ctx)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	topic := client.Topic(topicName)
	_, err = client.CreateSubscription(ctx, subName, pubsub.SubscriptionConfig{
		Topic: topic,
		PushConfig: pubsub.PushConfig{
			Endpoint: pushEndpoint,
		},
	})
	if status.Code(err) == codes.AlreadyExists {
		return nil
	}
	return err
}

func runPublisherLoop(ctx context.Context, db *sql.DB, publisher *pubsub.Topic, pollInterval time.Duration, batchSize int) {
	for {
		messages, err := jobdb.ClaimOutboxBatch(ctx, db, batchSize)
		if err != nil {
			slog.Error("outbox claim failed", "err", err)
			time.Sleep(pollInterval)
			continue
		}
		if len(messages) == 0 {
			time.Sleep(pollInterval)
			continue
		}

		for _, msg := range messages {
			result := publisher.Publish(ctx, &pubsub.Message{Data: msg.Payload})
			if _, err := result.Get(ctx); err != nil {
				_ = jobdb.RecordOutboxError(db, msg.ID, err.Error())
				continue
			}
			if err := jobdb.MarkOutboxPublished(db, msg.ID); err != nil {
				slog.Error("mark published failed for outbox", "outbox_id", msg.ID, "err", err)
			}
		}
	}
}

func fatal(msg string, attrs ...any) {
	slog.Error(msg, attrs...)
	os.Exit(1)
}
