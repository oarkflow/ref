package platform

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// OpenAPI builds an OpenAPI 3.1 document from the same route and shape
// declarations used by the runtime. The returned value can be JSON encoded.
func (p *Platform) OpenAPI() map[string]any {
	paths := map[string]any{}
	components := map[string]any{"schemas": map[string]any{}, "securitySchemes": map[string]any{
		"bearerAuth": map[string]any{
			"type":         "http",
			"scheme":       "bearer",
			"bearerFormat": "JWT",
			"description":  "JWT Bearer token",
		},
		"apiKeyAuth": map[string]any{
			"type":        "apiKey",
			"in":          "header",
			"name":        "X-API-Key",
			"description": "API key authentication header",
		},
		"cookieAuth": map[string]any{
			"type":        "apiKey",
			"in":          "cookie",
			"name":        "clear_session_id",
			"description": "Session cookie authentication",
		},
	}}
	schemaDefs := components["schemas"].(map[string]any)
	shapesMap := make(map[string]SchemaSpec, len(p.Document.Shapes))
	for _, shape := range p.Document.Shapes {
		shapesMap[shape.Name] = shape
		schemaDefs[shape.Name] = openAPISchema(shape)
	}
	schemaDefs["ErrorResponse"] = map[string]any{
		"type":        "object",
		"description": "Standard API error response",
		"properties": map[string]any{
			"success": map[string]any{"type": "boolean", "example": false},
			"code":    map[string]any{"type": "string", "example": "ERROR_CODE"},
			"message": map[string]any{"type": "string", "example": "Human readable error description"},
			"error":   map[string]any{"type": "string", "example": "Detailed internal error message"},
		},
		"required": []string{"success", "code", "message"},
	}
	intents := make(map[string]IntentSpec, len(p.Document.Intents))
	for _, it := range p.Document.Intents {
		intents[it.Name] = it
	}
	for _, route := range p.Document.Routes {
		path := openAPIPath(route.Path)
		item, _ := paths[path].(map[string]any)
		if item == nil {
			item = map[string]any{}
			paths[path] = item
		}
		method := strings.ToLower(route.Method)
		if method == "" {
			method = "get"
		}
		op := map[string]any{"operationId": route.Name, "responses": map[string]any{}}

		summary := route.Description
		desc := route.Description
		if it, ok := intents[route.Intent]; ok {
			if desc == "" && it.Description != "" {
				desc = it.Description
			}
			if summary == "" && it.Description != "" {
				summary = it.Description
			}
		}
		if summary == "" {
			summary = humanizeOperation(route.Name, route.Path)
		}
		if summary != "" {
			op["summary"] = summary
		}
		if desc != "" {
			op["description"] = desc
		}

		tags := route.Tags
		if len(tags) == 0 {
			tags = inferTags(route)
		}
		if len(tags) > 0 {
			op["tags"] = tags
		}

		params := make([]any, 0, len(route.Parameters)+len(pathParams(route.Path)))
		declared := map[string]bool{}
		for _, param := range route.Parameters {
			where := strings.ToLower(param.In)
			declared[where+":"+param.Name] = true
			ps := map[string]any{"name": param.Name, "in": where, "required": param.Required, "schema": parameterSchema(param)}
			if param.Description != "" {
				ps["description"] = param.Description
			}
			params = append(params, ps)
		}
		for _, name := range pathParams(route.Path) {
			if !declared["path:"+name] {
				params = append(params, map[string]any{"name": name, "in": "path", "required": true, "schema": map[string]any{"type": "string"}})
			}
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		if route.Auth != "" || (!route.AllowAnonymous && (route.Authz != nil || route.Session != "")) {
			op["security"] = []any{
				map[string]any{"cookieAuth": []string{}},
				map[string]any{"bearerAuth": []string{}},
				map[string]any{"apiKeyAuth": []string{}},
			}
		}
		responses := op["responses"].(map[string]any)
		status := route.Status
		if status == 0 {
			status = 200
		}
		if strings.EqualFold(route.Mode, "async") && status == 200 {
			status = 202
		}
		it := intents[route.Intent]
		respSchema, respExample := resolveResponseSchemaAndExample(it, route, shapesMap)

		response := map[string]any{
			"description": "Successful response",
			"content": map[string]any{
				"application/json": map[string]any{
					"schema":  respSchema,
					"example": respExample,
				},
			},
		}
		responses[fmt.Sprint(status)] = response

		for code, description := range map[string]string{
			"400": "Invalid request payload or parameters",
			"401": "Unauthenticated - missing or invalid credentials",
			"403": "Forbidden - insufficient permissions for this operation",
			"404": "Resource not found",
			"409": "Conflict - resource already exists or state lock active",
			"422": "Validation failed - input violates business rules",
			"429": "Rate limited - too many requests",
			"500": "Internal error",
			"503": "Service temporarily unavailable",
		} {
			errExample := map[string]any{
				"success": false,
				"code":    defaultErrorCode(code),
				"message": description,
				"error":   description,
			}
			responses[code] = map[string]any{
				"description": description,
				"content": map[string]any{
					"application/json": map[string]any{
						"schema":  map[string]any{"$ref": "#/components/schemas/ErrorResponse"},
						"example": errExample,
					},
				},
			}
		}
		if it.InputSchema != "" && method != "get" && method != "head" {
			var bodyExample any
			if inShape, ok := shapesMap[it.InputSchema]; ok {
				bodyExample = exampleFromShape(inShape)
			}
			contentObj := map[string]any{
				"schema": map[string]any{"$ref": "#/components/schemas/" + it.InputSchema},
			}
			if bodyExample != nil {
				contentObj["example"] = bodyExample
			}
			op["requestBody"] = map[string]any{
				"required": true,
				"content": map[string]any{
					"application/json": contentObj,
				},
			}
		}
		if route.MaxBodyBytes > 0 {
			op["x-max-body-bytes"] = route.MaxBodyBytes
		}
		item[method] = op
	}
	info := map[string]any{
		"title":       orDefault(p.Document.Name, "REF API"),
		"version":     orDefault(p.Document.Version, "1.0.0"),
		"description": "Compiled Runtime Execution Fabric (REF) API",
	}
	servers := []any{
		map[string]any{"url": "/", "description": "Current Environment"},
	}
	return map[string]any{"openapi": "3.1.0", "info": info, "servers": servers, "paths": paths, "components": components}
}

// WriteOpenAPI writes the generated API contract as stable, indented JSON.
func (p *Platform) WriteOpenAPI(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(p.OpenAPI())
}

func openAPIPath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if strings.HasPrefix(part, ":") {
			parts[i] = "{" + strings.TrimPrefix(part, ":") + "}"
		}
	}
	return strings.Join(parts, "/")
}
func parameterSchema(p HTTPParameterSpec) map[string]any {
	kind := p.Kind
	if kind == "" {
		kind = "string"
	}
	typ := openAPIType(kind)
	schema := map[string]any{"type": typ}
	if p.Format != "" {
		schema["format"] = p.Format
	}
	if len(p.Enum) > 0 {
		schema["enum"] = p.Enum
	}
	return schema
}
func openAPISchema(s SchemaSpec) map[string]any {
	typ := openAPIType(s.Kind)
	out := map[string]any{"type": typ}
	if s.Description != "" {
		out["description"] = s.Description
	}
	if typ != "object" {
		return out
	}
	props := map[string]any{}
	required := append([]string(nil), s.Required...)
	for _, f := range s.Props {
		props[f.Name] = openAPIField(f)
		if f.Required && !containsString(required, f.Name) {
			required = append(required, f.Name)
		}
	}
	out["properties"] = props
	if len(required) > 0 {
		out["required"] = required
	}
	if s.AdditionalProperties != nil {
		out["additionalProperties"] = *s.AdditionalProperties
	}
	return out
}
func openAPIField(f SchemaFieldSpec) map[string]any {
	kind := strings.ToLower(f.Kind)
	if kind == "" {
		if f.Schema != "" {
			kind = "object"
		} else if f.Items != "" {
			kind = "array"
		} else {
			kind = "any"
		}
	}
	if !isOpenAPIPrimitive(kind) {
		return map[string]any{"$ref": "#/components/schemas/" + f.Kind}
	}
	out := map[string]any{"type": openAPIType(kind)}
	if f.Description != "" {
		out["description"] = f.Description
	}
	if f.Format != "" {
		out["format"] = f.Format
	}
	if len(f.Enum) > 0 {
		out["enum"] = f.Enum
	}
	if f.Pattern != "" {
		out["pattern"] = f.Pattern
	}
	if f.Min != nil {
		out["minimum"] = *f.Min
	}
	if f.Max != nil {
		out["maximum"] = *f.Max
	}
	if f.MinLength != nil {
		out["minLength"] = *f.MinLength
	}
	if f.MaxLength != nil {
		out["maxLength"] = *f.MaxLength
	}
	if f.Default != nil {
		out["default"] = f.Default
	}
	if f.Required {
		out["x-required"] = true
	}
	if f.Sensitive {
		out["writeOnly"] = true
	}
	if f.Schema != "" {
		out = map[string]any{"$ref": "#/components/schemas/" + f.Schema}
	}
	if kind == "array" {
		item := strings.ToLower(f.Items)
		isPrim := isOpenAPIPrimitive(item)
		var schema map[string]any
		if isPrim {
			schema = map[string]any{"type": openAPIType(item)}
		} else if item != "" {
			schema = map[string]any{"$ref": "#/components/schemas/" + f.Items}
		} else {
			schema = map[string]any{}
		}
		out["items"] = schema
	}
	return out
}
func isOpenAPIPrimitive(t string) bool {
	switch strings.ToLower(t) {
	case "", "any", "string", "int", "integer", "number", "float", "bool", "boolean", "time", "date", "datetime", "object", "array":
		return true
	}
	return false
}
func openAPIType(t string) string {
	switch strings.ToLower(t) {
	case "", "any", "object":
		return "object"
	case "int", "integer":
		return "integer"
	case "float", "number":
		return "number"
	case "bool", "boolean":
		return "boolean"
	case "array":
		return "array"
	default:
		return "string"
	}
}
func containsString(items []string, target string) bool {
	for _, s := range items {
		if s == target {
			return true
		}
	}
	return false
}

func validateHTTPParameters(what string, route RouteSpec) error {
	method := strings.ToUpper(strings.TrimSpace(route.Method))
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "HEAD", "TRACE":
	default:
		return fmt.Errorf("ref/platform: %s: unsupported HTTP method %q", what, route.Method)
	}
	if !strings.HasPrefix(route.Path, "/") {
		return fmt.Errorf("ref/platform: %s: route path must begin with /", what)
	}
	seen := map[string]bool{}
	pathNames := map[string]bool{}
	for _, name := range pathParams(route.Path) {
		pathNames[name] = true
	}
	for _, p := range route.Parameters {
		where := strings.ToLower(strings.TrimSpace(p.In))
		if p.Name == "" {
			return fmt.Errorf("ref/platform: %s: HTTP parameter needs a name", what)
		}
		if where != "path" && where != "query" && where != "header" && where != "cookie" {
			return fmt.Errorf("ref/platform: %s: parameter %q has invalid in %q", what, p.Name, p.In)
		}
		key := where + ":" + p.Name
		if seen[key] {
			return fmt.Errorf("ref/platform: %s: duplicate parameter %q in %q", what, p.Name, where)
		}
		seen[key] = true
		if where == "path" && (!pathNames[p.Name] || !p.Required) {
			return fmt.Errorf("ref/platform: %s: path parameter %q must appear in the path and be required", what, p.Name)
		}
		if p.Kind != "" && !isOpenAPIPrimitive(strings.ToLower(p.Kind)) {
			return fmt.Errorf("ref/platform: %s: parameter %q has unsupported primitive kind %q", what, p.Name, p.Kind)
		}
	}
	return nil
}

func inferTags(route RouteSpec) []string {
	if len(route.Tags) > 0 {
		return route.Tags
	}
	name := strings.TrimPrefix(route.Name, "web_client.")
	parts := strings.Split(name, ".")
	if len(parts) > 1 && parts[0] != "" {
		return []string{formatTag(parts[0])}
	}
	path := strings.TrimPrefix(route.Path, "/web/client")
	path = strings.Trim(path, "/")
	segs := strings.Split(path, "/")
	if len(segs) > 0 && segs[0] != "" {
		return []string{formatTag(segs[0])}
	}
	return []string{"General"}
}

func formatTag(s string) string {
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, "-", " ")
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "auth", "login", "me":
		return "Authentication"
	case "coding":
		return "Medical Coding"
	case "qa":
		return "Quality Assurance"
	case "de", "dataentry", "data entry":
		return "Charge Data Entry"
	case "suspends", "suspend":
		return "Chart Suspensions"
	case "facilities", "facility":
		return "Facilities"
	case "workitem", "workitems":
		return "Workitems"
	case "provider", "providers":
		return "Healthcare Providers"
	case "charge code", "charge_code", "chargemaster":
		return "Chargemaster"
	case "cpt", "cpt code", "cpts":
		return "CPT & Medical Terminology"
	case "admin", "users", "roles", "companies":
		return "Administration & RBAC"
	default:
		words := strings.Fields(s)
		for i, w := range words {
			if len(w) > 0 {
				words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
			}
		}
		return strings.Join(words, " ")
	}
}

func humanizeOperation(name, path string) string {
	cleanName := strings.TrimPrefix(name, "web_client.")
	parts := strings.Split(cleanName, ".")
	target := cleanName
	if len(parts) > 1 {
		target = parts[len(parts)-1]
	}
	target = strings.ReplaceAll(target, "_", " ")
	target = strings.ReplaceAll(target, "-", " ")
	words := strings.Fields(target)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
		}
	}
	res := strings.Join(words, " ")
	if strings.HasPrefix(name, "web_client.") {
		res += " (Legacy Gateway)"
	}
	return res
}

func defaultErrorCode(code string) string {
	switch code {
	case "400":
		return "INVALID_REQUEST"
	case "401":
		return "UNAUTHENTICATED"
	case "403":
		return "FORBIDDEN"
	case "404":
		return "NOT_FOUND"
	case "409":
		return "CONFLICT"
	case "422":
		return "VALIDATION_FAILED"
	case "429":
		return "RATE_LIMITED"
	case "500":
		return "INTERNAL_ERROR"
	case "503":
		return "SERVICE_UNAVAILABLE"
	default:
		return "ERROR"
	}
}

func parseColumnsFromSQL(sql string) []string {
	sqlUpper := strings.ToUpper(sql)
	selectIdx := strings.Index(sqlUpper, "SELECT ")
	if selectIdx == -1 {
		return nil
	}
	fromIdx := strings.Index(sqlUpper, " FROM ")
	if fromIdx == -1 || fromIdx <= selectIdx+7 {
		return nil
	}
	colStr := sql[selectIdx+7 : fromIdx]
	parts := strings.Split(colStr, ",")
	var cols []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		words := strings.Fields(p)
		if len(words) > 0 {
			last := words[len(words)-1]
			last = strings.Trim(last, "\"`'")
			if dot := strings.LastIndex(last, "."); dot != -1 {
				last = last[dot+1:]
			}
			if last != "" && last != "*" {
				cols = append(cols, last)
			}
		}
	}
	return cols
}

func exampleFromShape(s SchemaSpec) map[string]any {
	ex := map[string]any{}
	for _, f := range s.Props {
		ex[f.Name] = exampleValueForField(f)
	}
	return ex
}

func exampleValueForField(f SchemaFieldSpec) any {
	if f.Default != nil {
		return f.Default
	}
	if len(f.Enum) > 0 {
		return f.Enum[0]
	}
	kind := strings.ToLower(f.Kind)
	switch kind {
	case "int", "integer":
		return 1
	case "float", "number":
		return 10.5
	case "bool", "boolean":
		return true
	case "array":
		return []any{}
	default:
		name := strings.ToLower(f.Name)
		if strings.Contains(name, "email") {
			return "user@example.com"
		}
		if strings.Contains(name, "password") {
			return "secret123"
		}
		if strings.HasSuffix(name, "_id") || name == "id" {
			return name + "_001"
		}
		if strings.Contains(name, "date") {
			return "2026-03-01"
		}
		if strings.Contains(name, "npi") {
			return "1234567890"
		}
		return "sample_" + f.Name
	}
}

func resolveResponseSchemaAndExample(it IntentSpec, route RouteSpec, shapes map[string]SchemaSpec) (map[string]any, any) {
	if it.OutputSchema != "" {
		ref := map[string]any{"$ref": "#/components/schemas/" + it.OutputSchema}
		if shape, ok := shapes[it.OutputSchema]; ok {
			return ref, exampleFromShape(shape)
		}
		return ref, map[string]any{"status": "ok"}
	}

	var respNode *NodeSpec
	for i := range it.Nodes {
		n := &it.Nodes[i]
		if n.Name == it.Response || n.Name == "response" || containsString(n.Provides, it.Response) {
			respNode = n
			break
		}
	}

	if respNode != nil {
		if respNode.Uses == "collect" && len(respNode.Config) > 0 {
			properties := map[string]any{}
			example := map[string]any{}

			for k, vAny := range respNode.Config {
				if k == "unwrap" {
					continue
				}
				v, _ := vAny.(string)

				var producer *NodeSpec
				for j := range it.Nodes {
					if containsString(it.Nodes[j].Provides, v) || it.Nodes[j].Name == v {
						producer = &it.Nodes[j]
						break
					}
				}

				if producer != nil && producer.Uses == "database.query" {
					sql, _ := producer.Config["statement"].(string)
					cols := parseColumnsFromSQL(sql)
					itemProps := map[string]any{}
					itemEx := map[string]any{}
					if len(cols) > 0 {
						for _, col := range cols {
							propType := "string"
							var val any = "sample_" + col
							if strings.HasSuffix(col, "_id") || col == "id" {
								val = col + "_1001"
							} else if strings.Contains(col, "count") || strings.Contains(col, "amount") || strings.Contains(col, "num") {
								propType = "number"
								val = 1
							} else if strings.HasPrefix(col, "is_") || strings.HasPrefix(col, "has_") {
								propType = "boolean"
								val = true
							} else if strings.HasSuffix(col, "_at") || strings.HasSuffix(col, "_date") {
								val = "2026-03-01T12:00:00Z"
							} else if col == "status" {
								val = "active"
							}
							itemProps[col] = map[string]any{"type": propType, "example": val}
							itemEx[col] = val
						}
					} else {
						itemProps["id"] = map[string]any{"type": "string", "example": "item_1"}
						itemEx["id"] = "item_1"
					}
					properties[k] = map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "object", "properties": itemProps},
					}
					example[k] = []any{itemEx}
				} else if producer != nil && producer.Uses == "database.exec" {
					properties[k] = map[string]any{"type": "integer", "example": 1}
					example[k] = 1
				} else if producer != nil && producer.Uses == "request.param" {
					properties[k] = map[string]any{"type": "string", "example": v}
					example[k] = v
				} else {
					if k == "status" {
						properties[k] = map[string]any{"type": "string", "example": "completed"}
						example[k] = "completed"
					} else if k == "success" {
						properties[k] = map[string]any{"type": "boolean", "example": true}
						example[k] = true
					} else if strings.HasSuffix(k, "_count") || k == "count" {
						properties[k] = map[string]any{"type": "integer", "example": 5}
						example[k] = 5
					} else if strings.HasSuffix(k, "_id") || k == "id" {
						properties[k] = map[string]any{"type": "string", "example": k + "_1001"}
						example[k] = k + "_1001"
					} else {
						properties[k] = map[string]any{"type": "string", "example": "sample_" + k}
						example[k] = "sample_" + k
					}
				}
			}

			if len(properties) > 0 {
				return map[string]any{"type": "object", "properties": properties}, example
			}
		}

		if respNode.Uses == "database.query" {
			sql, _ := respNode.Config["statement"].(string)
			cols := parseColumnsFromSQL(sql)
			itemProps := map[string]any{}
			itemEx := map[string]any{}
			for _, col := range cols {
				itemProps[col] = map[string]any{"type": "string", "example": "sample_" + col}
				itemEx[col] = "sample_" + col
			}
			return map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": itemProps}}, []any{itemEx}
		}
	}

	path := strings.ToLower(route.Path)
	if strings.HasSuffix(path, "list") || strings.HasSuffix(path, "facilities") || strings.HasSuffix(path, "workitem") || strings.HasSuffix(path, "types") || strings.HasSuffix(path, "providers") || strings.HasSuffix(path, "users") || strings.HasSuffix(path, "roles") || strings.HasSuffix(path, "companies") || strings.HasSuffix(path, "coders") {
		schema := map[string]any{
			"type": "object",
			"properties": map[string]any{
				"success": map[string]any{"type": "boolean", "example": true},
				"data": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "object"},
				},
			},
		}
		example := map[string]any{
			"success": true,
			"data": []any{
				map[string]any{"id": "rec_1001", "name": "sample_record", "status": "active"},
			},
		}
		return schema, example
	}

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"success": map[string]any{"type": "boolean", "example": true},
			"data":    map[string]any{"type": "object"},
			"message": map[string]any{"type": "string", "example": "success"},
		},
	}
	example := map[string]any{
		"success": true,
		"data":    map[string]any{"id": "rec_1001", "status": "active"},
		"message": "success",
	}
	return schema, example
}
