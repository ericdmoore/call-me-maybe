package syncwire

// The published OpenAPI fragment is mirrored from the hosted sync service.
// Pin the generator; normal builds do not invoke it or need network access.
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -generate types -package syncwire -o types.gen.go openapi.json
