OPENAPI_SPEC=openapi.yaml
OPENAPI_OUT=internal/api/api.gen.go
OPENAPI_PACKAGE=api

.PHONY: openapi
openapi:
	oapi-codegen -generate types,chi-server -package $(OPENAPI_PACKAGE) -o $(OPENAPI_OUT) $(OPENAPI_SPEC)
