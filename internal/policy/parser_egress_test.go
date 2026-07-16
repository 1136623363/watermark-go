package policy_test

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestProductionParserEgressUsesOnlyGuardedAdapters(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	config := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports,
		Dir: root, Tests: false,
	}
	loaded, err := packages.Load(config, "./internal/parser/...")
	if err != nil {
		t.Fatal(err)
	}
	var violations []string
	for _, current := range loaded {
		if len(current.Errors) != 0 {
			t.Fatalf("load parser package %s: %v", current.PkgPath, current.Errors)
		}
		for index, file := range current.Syntax {
			filename := current.CompiledGoFiles[index]
			if strings.HasSuffix(filename, "_test.go") || filepath.ToSlash(filename) == filepath.ToSlash(filepath.Join(root, "internal", "parser", "native", "http_client.go")) {
				continue
			}
			violations = append(violations, parserEgressViolations(file, current.TypesInfo, filename)...)
		}
	}
	if len(violations) != 0 {
		t.Fatalf("production parser bypasses guarded egress:\n%s", strings.Join(violations, "\n"))
	}
}

func TestParserEgressAnalyzerRejectsAliasesAndAllowsRequestConstruction(t *testing.T) {
	t.Parallel()
	unsafeFixtures := []string{
		`package fixture; import web "net/http"; var client = &web.Client{}`,
		`package fixture; import web "net/http"; func f(){ _, _ = web.Get("https://example.test") }`,
		`package fixture; import web "net/http"; func f(){ get := web.Get; _, _ = get("https://example.test") }`,
		`package fixture; import web "net/http"; var first = web.Get; var second = first; func f(){ _, _ = second("https://example.test") }`,
		`package fixture; import web "net/http"; func f(){ _ = web.DefaultClient; _ = web.DefaultTransport }`,
		`package fixture; import web "net/http"; func f(){ get := web.DefaultClient.Get; _, _ = get("https://example.test") }`,
		`package fixture; import . "net/http"; func f(){ _, _ = Get("https://example.test") }`,
		`package fixture; import . "net/http"; func f(){ get := Get; _, _ = get("https://example.test") }`,
		`package fixture; import . "net/http"; var first = Get; var second = first; func f(){ _, _ = second("https://example.test") }`,
		`package fixture; import . "net/http"; func f(){ _ = DefaultClient }`,
		`package fixture; import . "net/http"; func f(){ transport := DefaultTransport; _ = transport }`,
		`package fixture; import . "net/http"; func f(){ get := DefaultClient.Get; _, _ = get("https://example.test") }`,
		`package fixture; import web "net/http"; func f(){ _ = new(web.Client) }`,
		`package fixture; import env "os"; func f(){ _, _ = env.LookupEnv("TOKEN") }`,
		`package fixture; import env "os"; func f(){ lookup := env.LookupEnv; _, _ = lookup("TOKEN") }`,
		`package fixture; import . "os"; func f(){ _ = Getenv("TOKEN") }`,
		`package fixture; import . "os"; func f(){ _, _ = LookupEnv("TOKEN") }`,
		`package fixture; import . "os"; func f(){ _ = Environ() }`,
		`package fixture; import . "os"; func f(){ lookup := LookupEnv; _, _ = lookup("TOKEN") }`,
		`package fixture; import command "os/exec"; func f(){ _ = command.Command("tool") }`,
		`package fixture; import command "os/exec"; func f(){ run := command.Command; _ = run("tool") }`,
		`package fixture; import network "net"; func f(){ _, _ = network.Dial("tcp", "example.test:80") }`,
		`package fixture; import . "net"; func f(){ _, _ = Dial("tcp", "example.test:80") }`,
		`package fixture; import . "net"; func f(){ dial := Dial; _, _ = dial("tcp", "example.test:80") }`,
		`package fixture; import network "net"; func f(){ _ = new(network.Dialer) }`,
	}
	for index, source := range unsafeFixtures {
		if violations := analyzeParserEgressFixture(t, source); len(violations) == 0 {
			t.Fatalf("unsafe egress fixture %d bypassed the analyzer", index)
		}
	}
	safe := `package fixture; import "net/http"; func f(){ _, _ = http.NewRequest("GET", "https://example.test", nil) }`
	if violations := analyzeParserEgressFixture(t, safe); len(violations) != 0 {
		t.Fatalf("safe request construction rejected: %v", violations)
	}
	safeFixtures := []string{
		`package fixture; func Get(string) (int, error) { return 0, nil }; func f(){ _, _ = Get("local") }`,
		`package fixture; var DefaultClient = struct{}{}; var DefaultTransport = struct{}{}; func f(){ _, _ = DefaultClient, DefaultTransport }`,
		`package fixture; import . "net/http"; func f(){ Get := func(string) (int, error) { return 0, nil }; _, _ = Get("local"); _, _ = NewRequest("GET", "https://example.test", nil) }`,
		`package fixture; import . "os"; func f(){ Getenv := func(string) string { return "" }; _ = Getenv("TOKEN"); _ = PathSeparator }`,
	}
	for index, source := range safeFixtures {
		if violations := analyzeParserEgressFixture(t, source); len(violations) != 0 {
			t.Fatalf("safe shadowing fixture %d rejected: %v", index, violations)
		}
	}
}

func analyzeParserEgressFixture(t *testing.T, source string) []string {
	t.Helper()
	files := token.NewFileSet()
	file, err := parser.ParseFile(files, "fixture.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue),
		Uses:  make(map[*ast.Ident]types.Object),
		Defs:  make(map[*ast.Ident]types.Object),
	}
	configuration := types.Config{Importer: importer.Default()}
	if _, err := configuration.Check("fixture", files, []*ast.File{file}, info); err != nil {
		t.Fatal(err)
	}
	return parserEgressViolations(file, info, "fixture.go")
}

func parserEgressViolations(file *ast.File, info *types.Info, filename string) []string {
	var violations []string
	forbiddenAliases := parserForbiddenFunctionAliases(file, info)
	for _, specification := range file.Imports {
		path, err := strconv.Unquote(specification.Path.Value)
		if err == nil && path == "github.com/1136623363/watermark-go/internal/runtimecfg" {
			violations = append(violations, filename+": runtime configuration import")
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.CallExpr:
			switch functionExpression := unparenParserExpression(typed.Fun).(type) {
			case *ast.SelectorExpr:
				if label, forbidden := directForbiddenParserFunction(functionExpression, info); forbidden {
					violations = append(violations, fmt.Sprintf("%s: forbidden %s", filename, label))
				}
			case *ast.Ident:
				if functionExpression.Name == "new" && len(typed.Args) == 1 && forbiddenParserComposite(info.TypeOf(typed.Args[0])) {
					violations = append(violations, filename+": parser-local network client, transport, or dialer allocation")
					break
				}
				object := info.Uses[functionExpression]
				if label, forbidden := directForbiddenParserFunctionObject(object); forbidden {
					violations = append(violations, fmt.Sprintf("%s: forbidden %s", filename, label))
					break
				}
				if label, forbidden := forbiddenAliases[object]; forbidden {
					violations = append(violations, fmt.Sprintf("%s: forbidden function alias %s", filename, label))
				}
			}
		case *ast.Ident:
			if label, forbidden := forbiddenParserVariableObject(info.Uses[typed]); forbidden {
				violations = append(violations, fmt.Sprintf("%s: forbidden %s", filename, label))
			}
		case *ast.CompositeLit:
			if forbiddenParserComposite(info.TypeOf(typed)) {
				violations = append(violations, filename+": parser-local network client or transport")
			}
		}
		return true
	})
	return violations
}

func parserForbiddenFunctionAliases(file *ast.File, info *types.Info) map[types.Object]string {
	aliases := make(map[types.Object]string)
	type assignment struct {
		object types.Object
		value  ast.Expr
	}
	var assignments []assignment
	ast.Inspect(file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.ValueSpec:
			for index, name := range typed.Names {
				if len(typed.Values) == 0 {
					continue
				}
				valueIndex := index
				if valueIndex >= len(typed.Values) {
					valueIndex = len(typed.Values) - 1
				}
				assignments = append(assignments, assignment{object: info.Defs[name], value: typed.Values[valueIndex]})
			}
		case *ast.AssignStmt:
			for index, left := range typed.Lhs {
				name, ok := left.(*ast.Ident)
				if !ok || len(typed.Rhs) == 0 {
					continue
				}
				valueIndex := index
				if valueIndex >= len(typed.Rhs) {
					valueIndex = len(typed.Rhs) - 1
				}
				object := info.Defs[name]
				if object == nil {
					object = info.Uses[name]
				}
				assignments = append(assignments, assignment{object: object, value: typed.Rhs[valueIndex]})
			}
		}
		return true
	})

	for changed := true; changed; {
		changed = false
		for _, current := range assignments {
			if current.object == nil {
				continue
			}
			if _, exists := aliases[current.object]; exists {
				continue
			}
			label, forbidden := parserForbiddenFunctionExpression(current.value, info, aliases)
			if !forbidden {
				continue
			}
			aliases[current.object] = label
			changed = true
		}
	}
	return aliases
}

func parserForbiddenFunctionExpression(expression ast.Expr, info *types.Info, aliases map[types.Object]string) (string, bool) {
	switch typed := unparenParserExpression(expression).(type) {
	case *ast.SelectorExpr:
		return directForbiddenParserFunction(typed, info)
	case *ast.Ident:
		if label, forbidden := directForbiddenParserFunctionObject(info.Uses[typed]); forbidden {
			return label, true
		}
		label, forbidden := aliases[info.Uses[typed]]
		return label, forbidden
	default:
		return "", false
	}
}

func directForbiddenParserFunction(selector *ast.SelectorExpr, info *types.Info) (string, bool) {
	return directForbiddenParserFunctionObject(info.Uses[selector.Sel])
}

func directForbiddenParserFunctionObject(object types.Object) (string, bool) {
	function, ok := object.(*types.Func)
	if !ok || function.Pkg() == nil {
		return "", false
	}
	signature, _ := function.Type().(*types.Signature)
	packageFunction := signature == nil || signature.Recv() == nil
	if !forbiddenParserFunction(function.Pkg().Path(), function.Name(), packageFunction) {
		return "", false
	}
	return function.Pkg().Path() + "." + function.Name(), true
}

func forbiddenParserVariableObject(object types.Object) (string, bool) {
	variable, ok := object.(*types.Var)
	if !ok || variable.Pkg() == nil || variable.Pkg().Path() != "net/http" {
		return "", false
	}
	if variable.Name() != "DefaultClient" && variable.Name() != "DefaultTransport" {
		return "", false
	}
	return variable.Pkg().Path() + "." + variable.Name(), true
}

func unparenParserExpression(expression ast.Expr) ast.Expr {
	for {
		parenthesized, ok := expression.(*ast.ParenExpr)
		if !ok {
			return expression
		}
		expression = parenthesized.X
	}
}

func forbiddenParserFunction(path, name string, packageFunction bool) bool {
	switch path {
	case "os":
		return name == "Getenv" || name == "LookupEnv" || name == "Environ"
	case "os/exec":
		return name == "Command" || name == "CommandContext"
	case "net":
		return strings.HasPrefix(name, "Dial")
	case "net/http":
		return packageFunction && (name == "Get" || name == "Post" || name == "PostForm" || name == "Head")
	case "github.com/go-resty/resty/v2":
		return packageFunction && (name == "New" || name == "NewWithClient")
	default:
		return false
	}
}

func forbiddenParserComposite(value types.Type) bool {
	pointer, ok := value.(*types.Pointer)
	if ok {
		value = pointer.Elem()
	}
	named, ok := value.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return false
	}
	path, name := named.Obj().Pkg().Path(), named.Obj().Name()
	return (path == "net/http" && (name == "Client" || name == "Transport")) ||
		(path == "net" && name == "Dialer") ||
		(path == "github.com/go-resty/resty/v2" && name == "Client")
}
