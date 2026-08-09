package openapiimport

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseSupportsOpenAPI30AndResolvesInternalReferences(t *testing.T) {
	const document = `openapi: 3.0.3
info:
  title: Pet API
  version: "1.2"
servers:
  - url: https://api.example.com/v1
paths:
  /pets/{petId}:
    get:
      operationId: getPet
      parameters:
        - $ref: '#/components/parameters/PetID'
        - name: include
          in: query
          schema:
            $ref: '#/components/schemas/Include'
      responses:
        '200':
          description: A pet
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/Pet'
  /pets:
    post:
      operationId: createPet
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/Pet'
      responses:
        '201':
          description: Created
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/Pet'
components:
  parameters:
    PetID:
      name: petId
      in: path
      required: true
      description: Pet identifier
      schema:
        type: string
  schemas:
    Include:
      type: string
    Pet:
      type: object
      required: [id]
      properties:
        id:
          type: string
        name:
          type: string
`
	preview, err := Parse([]byte(document))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if preview.OpenAPIVersion != "3.0.3" || preview.Title != "Pet API" || preview.Version != "1.2" || len(preview.Servers) != 1 {
		t.Fatalf("preview metadata = %#v", preview)
	}
	if len(preview.Operations) != 2 {
		t.Fatalf("operations = %#v, want GET and POST", preview.Operations)
	}
	operations := make(map[string]Operation, len(preview.Operations))
	for _, operation := range preview.Operations {
		operations[operation.Key] = operation
	}
	get := operations["GET /pets/{petId}"]
	if get.Key != "GET /pets/{petId}" || !get.Eligible || get.SuggestedToolName != "getPet" {
		t.Fatalf("GET operation = %#v", get)
	}
	if len(get.Tool.Parameters) != 2 || get.Tool.Parameters[0].Name != "petId" || get.Tool.Parameters[0].Argument != "petId" || get.Tool.Parameters[0].Schema["type"] != "string" {
		t.Fatalf("resolved GET parameters = %#v", get.Tool.Parameters)
	}
	if get.Tool.Parameters[1].Schema["type"] != "string" {
		t.Fatalf("resolved query schema = %#v", get.Tool.Parameters[1].Schema)
	}
	if output := get.Tool.OutputSchema; output == nil || output["type"] != "object" {
		t.Fatalf("resolved output schema = %#v", output)
	}
	post := operations["POST /pets"]
	if post.Key != "POST /pets" || !post.Eligible || !post.Tool.BodyRequired || post.Tool.BodySchema["type"] != "object" {
		t.Fatalf("POST operation = %#v", post)
	}
	if preview.SHA256 == "" || preview.Document != document {
		t.Fatalf("document digest/preservation = %q/%t", preview.SHA256, preview.Document == document)
	}
}

func TestParseSupportsOpenAPI31(t *testing.T) {
	const document = `openapi: 3.1.0
info:
  title: Status API
  version: "2026.1"
paths:
  /health:
    get:
      summary: Health check
      responses:
        '200':
          description: Healthy
          content:
            application/json:
              schema:
                type: object
                properties:
                  healthy:
                    type: boolean
`
	preview, err := Parse([]byte(document))
	if err != nil {
		t.Fatalf("Parse() OpenAPI 3.1 error: %v", err)
	}
	if preview.OpenAPIVersion != "3.1.0" || len(preview.Operations) != 1 || !preview.Operations[0].Eligible {
		t.Fatalf("OpenAPI 3.1 preview = %#v", preview)
	}
	if preview.Operations[0].SuggestedToolName != "get_health" {
		t.Fatalf("generated operation name = %q", preview.Operations[0].SuggestedToolName)
	}
}

func TestParsePreservesLargeSchemaNumbers(t *testing.T) {
	const document = `openapi: 3.0.3
info: {title: Precise, version: "1"}
paths:
  /items:
    get:
      operationId: listItems
      parameters:
        - name: limit
          in: query
          schema:
            type: integer
            default: 9007199254740993
            minimum: 9007199254740993
      responses:
        '200': {description: ok}
`
	preview, err := Parse([]byte(document))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if len(preview.Operations) != 1 || !preview.Operations[0].Eligible {
		t.Fatalf("precise operation = %#v", preview.Operations)
	}
	schema := preview.Operations[0].Tool.Parameters[0].Schema
	encoded, err := json.Marshal(schema)
	if err != nil || string(encoded) != `{"default":9007199254740993,"minimum":9007199254740993,"type":"integer"}` {
		t.Fatalf("large schema number encoding = %s, err=%v", encoded, err)
	}
}

func TestParseRejectsExternalReferencesAndMarksUnsupportedOperationsIneligible(t *testing.T) {
	const external = `openapi: 3.0.3
info: {title: External, version: "1"}
paths:
  /pets:
    get:
      responses:
        '200':
          description: ok
          content:
            application/json:
              schema:
                $ref: https://example.invalid/pet.json#/Pet
`
	if _, err := Parse([]byte(external)); err == nil {
		t.Fatalf("external reference error = %v", err)
	}

	const unsupported = `openapi: 3.0.3
info: {title: Unsupported, version: "1"}
paths:
  /pets:
    options:
      operationId: ignoredOptions
      responses:
        '204': {description: no content}
    get:
      operationId: cookieOperation
      parameters:
        - name: sid
          in: cookie
          required: true
          schema: {type: string}
      responses:
        '200': {description: ok}
`
	preview, err := Parse([]byte(unsupported))
	if err != nil {
		t.Fatalf("Parse() unsupported operation document error: %v", err)
	}
	if len(preview.Operations) != 1 || preview.Operations[0].Key != "GET /pets" {
		t.Fatalf("unsupported operations were not filtered = %#v", preview.Operations)
	}
	if preview.Operations[0].Eligible || !strings.Contains(preview.Operations[0].Reason, "only path, query, and header") {
		t.Fatalf("unsupported parameter operation = %#v", preview.Operations[0])
	}
}

func TestParseRejectsCallbacksAndWebhooks(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		feature string
	}{
		{
			name: "callback",
			doc: `openapi: 3.0.3
info: {title: Callback, version: "1"}
paths:
  /events:
    post:
      callbacks:
        onEvent:
          '{$request.body#/callbackUrl}':
            post:
              responses:
                '200': {description: received}
      responses:
        '202': {description: accepted}
`,
			feature: "callbacks",
		},
		{
			name: "webhook",
			doc: `openapi: 3.1.0
info: {title: Webhook, version: "1"}
webhooks:
  onEvent:
    post:
      responses:
        '200': {description: received}
paths: {}
`,
			feature: "webhooks",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse([]byte(tt.doc)); err == nil || !strings.Contains(strings.ToLower(err.Error()), tt.feature) {
				t.Fatalf("Parse() %s error = %v", tt.feature, err)
			}
		})
	}
}

func TestParseOverridesPathParametersAndRejectsUnsupportedParameterSchemas(t *testing.T) {
	const document = `openapi: 3.0.3
info: {title: Parameters, version: "1"}
paths:
  /items/{id}:
    parameters:
      - name: id
        in: path
        required: true
        description: path-level
        schema: {type: string}
    get:
      operationId: getItem
      parameters:
        - name: id
          in: path
          required: true
          description: operation-level
          schema: {type: integer}
      responses:
        '200': {description: ok}
  /search:
    get:
      operationId: search
      parameters:
        - name: filter
          in: query
          schema:
            type: object
            properties:
              term: {type: string}
      responses:
        '200': {description: ok}
  /array:
    get:
      operationId: arraySearch
      parameters:
        - name: tags
          in: query
          explode: false
          schema:
            type: array
            items: {type: string}
      responses:
        '200': {description: ok}
  /reserved:
    get:
      operationId: reservedSearch
      parameters:
        - name: filter
          in: query
          allowReserved: true
          schema: {type: string}
      responses:
        '200': {description: ok}
`
	preview, err := Parse([]byte(document))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	operations := make(map[string]Operation, len(preview.Operations))
	for _, operation := range preview.Operations {
		operations[operation.Key] = operation
	}
	overridden := operations["GET /items/{id}"]
	if !overridden.Eligible || len(overridden.Tool.Parameters) != 1 || overridden.Tool.Parameters[0].Schema["type"] != "integer" || overridden.Tool.Parameters[0].Description != "operation-level" {
		t.Fatalf("operation parameter override = %#v", overridden)
	}
	uneligible := operations["GET /search"]
	if uneligible.Eligible || !strings.Contains(uneligible.Reason, "parameter schemas") {
		t.Fatalf("unsupported parameter schema operation = %#v", uneligible)
	}
	array := operations["GET /array"]
	if array.Eligible || !strings.Contains(array.Reason, "explode=false") {
		t.Fatalf("query array explode=false operation = %#v", array)
	}
	reserved := operations["GET /reserved"]
	if reserved.Eligible || !strings.Contains(reserved.Reason, "allowReserved") {
		t.Fatalf("allowReserved query operation = %#v", reserved)
	}
}

func TestParseRejectsFormMultipartAndBinaryRequestBodies(t *testing.T) {
	const document = `openapi: 3.0.3
info: {title: Bodies, version: "1"}
paths:
  /form:
    post:
      requestBody:
        content:
          application/x-www-form-urlencoded:
            schema: {type: object}
      responses:
        '200': {description: ok}
  /multipart:
    post:
      requestBody:
        content:
          multipart/form-data:
            schema: {type: object}
      responses:
        '200': {description: ok}
  /binary:
    post:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                payload: {type: string, format: binary}
      responses:
        '200': {description: ok}
`
	preview, err := Parse([]byte(document))
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	wantReasons := map[string]string{
		"POST /form":      "multipart and form",
		"POST /multipart": "multipart and form",
		"POST /binary":    "file and binary",
	}
	if len(preview.Operations) != len(wantReasons) {
		t.Fatalf("body operations = %#v", preview.Operations)
	}
	for _, operation := range preview.Operations {
		wantReason, ok := wantReasons[operation.Key]
		if !ok || operation.Eligible || !strings.Contains(operation.Reason, wantReason) {
			t.Fatalf("request body boundary operation = %#v", operation)
		}
	}
}
