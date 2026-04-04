package parse

import (
	"strings"

	"github.com/alecthomas/participle/v2"
	"github.com/alecthomas/participle/v2/lexer"
)

var agentfileLexer = lexer.MustStateful(lexer.Rules{ //nolint:gochecknoglobals // participle grammar
	"Root": {
		{Name: "Whitespace", Pattern: `[ \t]+`},
		{Name: "String", Pattern: `"(?:[^"\\]|\\.)*"`},
		{Name: "FileRef", Pattern: `file://[^\s]+`},
		{Name: "Equals", Pattern: `=`},
		{Name: "Bang", Pattern: `!`},
		{Name: "Ident", Pattern: `[^\s"=!]+`},
	},
})

var instructionParser = participle.MustBuild[instruction]( //nolint:gochecknoglobals // participle grammar
	participle.Lexer(agentfileLexer),
	participle.Elide("Whitespace"),
	participle.Unquote("String"),
	participle.Map(func(t lexer.Token) (lexer.Token, error) {
		t.Value = strings.TrimPrefix(t.Value, "file://")
		return t, nil
	}, "FileRef"),
)

type instruction struct {
	From    *string      `  "FROM" @Ident`
	Runtime *string      `| "RUNTIME" @Ident`
	Model   *string      `| "MODEL" @Ident`
	Name    *string      `| "NAME" @Ident`
	Context *contextInst `| @@`
	Config  *configInst  `| @@`
	Bin     *binInst     `| @@`
	Add     *addInst     `| @@`
	Label   *labelInst   `| @@`
	Arg     *argInst     `| @@`
}

type contextInst struct {
	Name string  `"CONTEXT" @Ident`
	Desc *string `@String?`
	File *string `@FileRef?`
}

type configInst struct {
	Key      string  `"CONFIG" @Ident`
	Required bool    `@"!"?`
	Value    *string `( "=" @( Ident | String ) )?`
	Desc     *string `@String?`
}

type binInst struct {
	Name  string  `"BIN" @Ident`
	Image string  `@Ident`
	Desc  *string `@String?`
}

type addInst struct {
	Src  string  `"ADD" @Ident`
	Dst  string  `@Ident`
	Desc *string `@String?`
}

type labelInst struct {
	Key   string `"LABEL" @Ident "="`
	Value string `@( Ident | String )`
}

type argInst struct {
	Key   string  `"ARG" @Ident`
	Value *string `( "=" @( Ident | String ) )?`
}
