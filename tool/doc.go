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
package tool
