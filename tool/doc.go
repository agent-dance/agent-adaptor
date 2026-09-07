// Package tool defines provider-neutral tools implemented by Go functions.
//
// A tool is declared once and installed on an Agent with adaptor.WithTools:
//
//	search := tool.Define(
//		"search_repo",
//		"Search files in the current repository.",
//		func(ctx context.Context, in SearchInput) (SearchOutput, error) {
//			return searchRepo(ctx, in.Query)
//		},
//		tool.ReadOnly(),
//		tool.Idempotent(),
//		tool.Revision("search_repo/v1"),
//	)
//
// Input and output JSON Schemas are inferred from the handler's Go types.
// InputSchemaJSON and OutputSchemaJSON are provider-neutral escape hatches for
// schemas maintained outside Go. Transport, endpoint authentication, and
// runtime lifecycle are deliberately not part of this package's vocabulary.
//
// Invalid JSON, input schema mismatches, and Go input decoding failures stop
// before the handler and match ErrInvalidInput. AsRejection also recognizes
// their invalid_input code and safe correction: syntax failures ask for valid
// JSON, schema failures ask about required fields, types and allowed values,
// and Go decoding failures ask about input types and formats. Extra fields may
// identify the lexicographically first extra name, bounded to 64 UTF-8 bytes
// plus an ellipsis and quoted with controls and non-printing characters escaped.
// Failures inside anyOf/oneOf alternatives use the generic schema correction
// because another branch may declare the field.
// These corrections never include schema contents, values, or underlying error
// text. Handler errors, panics, and output validation errors remain private to
// the runtime unless the handler explicitly returns Reject.
package tool
