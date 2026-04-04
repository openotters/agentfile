// Package tools extracts tool binaries and metadata from OCI image manifests.
//
// Tools does not perform network operations. The caller provides a content.Fetcher
// (e.g. *memory.Store, *remote.Repository) that already contains the tool image.
//
//	Tool OCI Image (already fetched)
//	+--------------------------------------------------+
//	| manifest                                         |
//	|   annotations:                                   |
//	|     vnd.openotters.tool.bin = "jq"               |
//	|     vnd.openotters.tool.description = "..."      |
//	|     vnd.openotters.tool.usage = "USAGE.md"       |
//	|                                                  |
//	|   layers:                                        |
//	|     [0] jq        (binary, title annotation)     |
//	|     [1] USAGE.md  (optional, title annotation)   |
//	+--------------------------------------------------+
//	              |
//	              v
//	  Info(manifest)          -> ToolInfo (description, usage path, bin path)
//	  ExtractBin(fetcher, …) -> writes binary to billy.Filesystem
//	  FetchUsage(fetcher, …) -> returns USAGE.md content as string
package tool
