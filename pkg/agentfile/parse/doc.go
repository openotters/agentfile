// Package parse reads Agentfiles into structured [pkg.Agentfile] values.
//
// Parsing is a two-phase process: a line scanner handles comments, heredocs,
// and ARG expansion, then each instruction line is parsed by a participle v2
// grammar. Semantic validation runs after parsing.
//
//	Agentfile (text)
//	      |
//	      v
//	+-------------+
//	| Line Scanner |  comments, blank lines, # syntax=
//	+-------------+
//	      |
//	      v  (per instruction line)
//	+-------------+
//	|  Heredoc    |  extract <<MARKER content
//	|  Extractor  |
//	+-------------+
//	      |
//	      v
//	+-------------+
//	|  ARG        |  ${VAR} substitution
//	|  Expansion  |
//	+-------------+
//	      |
//	      v
//	+-------------+
//	| Participle  |  grammar-based instruction parsing
//	|  Parser     |  FROM, RUNTIME, MODEL, NAME, CONTEXT,
//	|             |  CONFIG, BIN, ADD, LABEL, ARG
//	+-------------+
//	      |
//	      v
//	+-------------+
//	| Validation  |  FROM first, reserved names, required
//	|             |  config constraints
//	+-------------+
//	      |
//	      v
//	  *pkg.Agentfile
package parse
