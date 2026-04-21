package web

import "testing"

func boolPtr(v bool) *bool { return &v }

func TestBuildSuggestFallback_ShippingKEPPalette(t *testing.T) {
	task := "Bitte entscheide für BK_Product ob KEP-Paket oder Palette-Spedition nötig ist."
	got := buildSuggestFallback(task, "")

	if got.ResponseFormat != "json" {
		t.Fatalf("response_format=%q, want json", got.ResponseFormat)
	}
	if got.ResponseSchema == nil {
		t.Fatal("expected response_schema")
	}
	if len(got.ResponseSchema) != 1 || got.ResponseSchema["versandart"] != "KEP|Palette" {
		t.Fatalf("unexpected response_schema: %#v", got.ResponseSchema)
	}
	if got.OutputFields != "BK_Product, versandart" {
		t.Fatalf("output_fields=%q, want BK_Product, versandart", got.OutputFields)
	}
	if got.IncludeInputInOutput != "key" {
		t.Fatalf("include_input_in_output=%q, want key", got.IncludeInputInOutput)
	}
	if got.KeyColumn != "BK_Product" {
		t.Fatalf("key_column=%q, want BK_Product", got.KeyColumn)
	}
	if got.StoreRawResponse == nil || *got.StoreRawResponse {
		t.Fatalf("store_raw_response=%v, want false", got.StoreRawResponse)
	}
	if got.StrictOutput == nil || !*got.StrictOutput {
		t.Fatalf("strict_output=%v, want true", got.StrictOutput)
	}
}

func TestNormalizeSuggestResponse_ShippingTask(t *testing.T) {
	sr := suggestResponse{
		ResponseFormat: "csv",
		ResponseSchema: map[string]string{
			"debug_reason": "one sentence",
		},
		OutputFields: "BK_Product",
	}
	got := normalizeSuggestResponse("Bitte für BK_Product KEP oder Palette entscheiden.", sr)

	if got.ResponseFormat != "json" {
		t.Fatalf("response_format=%q, want json", got.ResponseFormat)
	}
	if got.ResponseSchema["versandart"] != "KEP|Palette" {
		t.Fatalf("response_schema.versandart=%q, want KEP|Palette", got.ResponseSchema["versandart"])
	}
	if got.OutputFields != "BK_Product, versandart, debug_reason" {
		t.Fatalf("output_fields=%q, want BK_Product, versandart, debug_reason", got.OutputFields)
	}
	if got.IncludeInputInOutput != "key" || got.KeyColumn != "BK_Product" {
		t.Fatalf("unexpected key output settings: mode=%q key=%q", got.IncludeInputInOutput, got.KeyColumn)
	}
	if got.StoreRawResponse == nil || *got.StoreRawResponse {
		t.Fatalf("store_raw_response=%v, want false", got.StoreRawResponse)
	}
	if got.StrictOutput == nil || !*got.StrictOutput {
		t.Fatalf("strict_output=%v, want true", got.StrictOutput)
	}
}

func TestNormalizeSuggestResponse_StripsFormatPromptLinesAndSetsOutputType(t *testing.T) {
	sr := suggestResponse{
		PrePrompt:  "Aufgabe: klassifiziere Artikel.\nBitte Ausgabe als CSV.",
		PostPrompt: "Please output as CSV with two columns.",
	}
	got := normalizeSuggestResponse("Bitte Ausgabe als CSV", sr)

	if got.PrePrompt == "" {
		t.Fatal("expected non-empty semantic pre_prompt")
	}
	if got.PostPrompt != "" {
		t.Fatalf("expected post_prompt to be stripped, got %q", got.PostPrompt)
	}
	if got.ResponseFormat != "csv" {
		t.Fatalf("response_format=%q, want csv", got.ResponseFormat)
	}
	if got.OutputType != "csv" {
		t.Fatalf("output_type=%q, want csv", got.OutputType)
	}
}

func TestBuildSuggestFallback_DetectsOutputTypeFromTask(t *testing.T) {
	got := buildSuggestFallback("Bitte Ausgabe als CSV mit BK_Product", "")
	if got.OutputType != "csv" {
		t.Fatalf("output_type=%q, want csv", got.OutputType)
	}
	if got.PrePrompt == "" {
		t.Fatal("expected pre_prompt")
	}
	if got.PrePrompt == "Aufgabe: Bitte Ausgabe als CSV mit BK_Product" {
		t.Fatalf("expected format instruction to be removed from pre_prompt, got %q", got.PrePrompt)
	}
}

func TestNormalizeSuggestResponse_RemovesRawAndAliasFromOutputFields(t *testing.T) {
	sr := suggestResponse{
		ResponseFormat: "json",
		ResponseField:  "llm_response",
		ResponseSchema: map[string]string{
			"debug_reason":    "one sentence",
			"shipping_method": "KEP|Palette",
			"versandart":      "KEP|Palette",
		},
		OutputFields:     "debug_reason, llm_response, shipping_method, versandart",
		StoreRawResponse: boolPtr(false),
	}
	got := normalizeSuggestResponse("Classify shipping method", sr)

	if _, ok := got.ResponseSchema["shipping_method"]; ok {
		t.Fatalf("expected shipping_method alias to be removed from response_schema: %#v", got.ResponseSchema)
	}
	if got.OutputFields != "debug_reason, versandart" {
		t.Fatalf("output_fields=%q, want debug_reason, versandart", got.OutputFields)
	}
}

func TestNormalizeSuggestResponse_ShippingPresetProducesLeanColumns(t *testing.T) {
	sr := suggestResponse{
		ResponseFormat: "json",
		ResponseField:  "llm_response",
		ResponseSchema: map[string]string{
			"debug_reason":    "one sentence",
			"shipping_method": "KEP|Palette",
		},
		OutputFields:     "debug_reason, llm_response, shipping_method, versandart",
		StoreRawResponse: boolPtr(true),
	}
	got := normalizeSuggestResponse("Entscheide für BK_Product zwischen KEP Paket und Palette Spedition", sr)

	if got.StoreRawResponse == nil || *got.StoreRawResponse {
		t.Fatalf("expected store_raw_response=false, got %v", got.StoreRawResponse)
	}
	if got.OutputFields != "BK_Product, versandart, debug_reason" {
		t.Fatalf("output_fields=%q, want BK_Product, versandart, debug_reason", got.OutputFields)
	}
}
