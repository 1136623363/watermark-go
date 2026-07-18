package policy_test

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestProductionNetworkEgressUsesNetguardOrTypedConnectors(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	config := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports,
		Dir: root, Tests: false,
	}
	loaded, err := packages.Load(config, "./cmd/...", "./internal/...")
	if err != nil {
		t.Fatal(err)
	}
	var violations []string
	for _, current := range loaded {
		if len(current.Errors) != 0 {
			t.Fatalf("load production package %s: %v", current.PkgPath, current.Errors)
		}
		for index, file := range current.Syntax {
			filename := current.CompiledGoFiles[index]
			if strings.HasSuffix(filename, "_test.go") {
				continue
			}
			violations = append(violations, productionEgressViolations(root, filename, file, current.TypesInfo)...)
		}
	}
	sort.Strings(violations)
	if len(violations) != 0 {
		t.Fatalf("production network egress bypasses Task 4 guardrails:\n%s", strings.Join(violations, "\n"))
	}
}

func TestProductionNetworkEgressAnalyzerRejectsAliasDotImportAndSubprocessBypasses(t *testing.T) {
	t.Parallel()
	unsafeFixtures := []string{
		`package fixture; import web "net/http"; var client = &web.Client{}`,
		`package fixture; import web "net/http"; var transport = web.Transport{}`,
		`package fixture; import web "net/http"; func f(){ _, _ = web.Get("https://example.test") }`,
		`package fixture; import web "net/http"; func f(){ _ = web.DefaultClient; _ = web.DefaultTransport }`,
		`package fixture; import . "net/http"; func f(){ _, _ = Get("https://example.test") }`,
		`package fixture; import . "net/http"; var transport = Transport{}`,
		`package fixture; import network "net"; func f(){ _, _ = network.Dial("tcp", "example.test:443") }`,
		`package fixture; import . "net"; func f(){ d := Dialer{}; _ = d }`,
		`package fixture; import command "os/exec"; func f(){ _ = command.Command("sh", "-c", "curl https://example.test") }`,
		`package fixture; import . "os/exec"; func f(){ _ = Command("curl", "https://example.test") }`,
		`package fixture; import "crypto/tls"; func f(){ _ = tls.Config{InsecureSkipVerify: true} }`,
		`package fixture; import "net/http"; type rawTransport struct{}; func (rawTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, nil }`,
	}
	for index, source := range unsafeFixtures {
		if violations := analyzeProductionEgressFixture(t, source); len(violations) == 0 {
			t.Fatalf("unsafe fixture %d bypassed analyzer", index)
		}
	}
	safeFixtures := []string{
		`package fixture; import "net/http"; func f(){ _, _ = http.NewRequest("GET", "https://example.test", nil) }`,
		`package fixture; type Client struct{}; func f(){ _ = &Client{} }`,
	}
	for index, source := range safeFixtures {
		if violations := analyzeProductionEgressFixture(t, source); len(violations) != 0 {
			t.Fatalf("safe fixture %d rejected: %v", index, violations)
		}
	}
}

func analyzeProductionEgressFixture(t *testing.T, source string) []string {
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
	return productionEgressViolations("", "fixture.go", file, info)
}

func productionEgressViolations(root, filename string, file *ast.File, info *types.Info) []string {
	violations := productionRoundTripViolations(root, filename, file, info)
	if productionEgressPrimitiveAllowed(root, filename) {
		return violations
	}
	aliases := forbiddenProductionFunctionAliases(file, info)
	ast.Inspect(file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.CallExpr:
			switch functionExpression := unparenProductionExpression(typed.Fun).(type) {
			case *ast.SelectorExpr:
				if label, forbidden := directForbiddenProductionFunction(functionExpression, info); forbidden {
					violations = append(violations, fmt.Sprintf("%s: forbidden %s", filename, label))
				}
			case *ast.Ident:
				if functionExpression.Name == "new" && len(typed.Args) == 1 && forbiddenProductionComposite(info.TypeOf(typed.Args[0])) {
					violations = append(violations, filename+": production network primitive allocation")
					break
				}
				object := info.Uses[functionExpression]
				if label, forbidden := directForbiddenProductionFunctionObject(object); forbidden {
					violations = append(violations, fmt.Sprintf("%s: forbidden %s", filename, label))
					break
				}
				if label, forbidden := aliases[object]; forbidden {
					violations = append(violations, fmt.Sprintf("%s: forbidden function alias %s", filename, label))
				}
			}
		case *ast.Ident:
			if label, forbidden := forbiddenProductionVariableObject(info.Uses[typed]); forbidden {
				violations = append(violations, fmt.Sprintf("%s: forbidden %s", filename, label))
			}
		case *ast.CompositeLit:
			if forbiddenProductionComposite(info.TypeOf(typed)) {
				violations = append(violations, filename+": production network primitive allocation")
			}
			if insecureTLSComposite(typed, info) {
				violations = append(violations, filename+": forbidden crypto/tls.Config.InsecureSkipVerify")
			}
		}
		return true
	})
	return violations
}

func productionRoundTripViolations(root, filename string, file *ast.File, info *types.Info) []string {
	var violations []string
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv == nil || function.Name == nil || function.Name.Name != "RoundTrip" {
			continue
		}
		object, ok := info.Defs[function.Name].(*types.Func)
		if !ok || !isHTTPRoundTripperMethod(object) {
			continue
		}
		receiver := roundTripReceiverName(object)
		if productionRoundTripAllowed(root, filename, receiver) {
			continue
		}
		violations = append(violations, fmt.Sprintf("%s: forbidden custom RoundTrip transport %s", filename, receiver))
	}
	return violations
}

func isHTTPRoundTripperMethod(function *types.Func) bool {
	if function == nil {
		return false
	}
	signature, ok := function.Type().(*types.Signature)
	if !ok || signature.Recv() == nil || signature.Params().Len() != 1 || signature.Results().Len() != 2 {
		return false
	}
	requestPointer, ok := signature.Params().At(0).Type().(*types.Pointer)
	if !ok || !typeNamed(requestPointer.Elem(), "net/http", "Request") {
		return false
	}
	responsePointer, ok := signature.Results().At(0).Type().(*types.Pointer)
	if !ok || !typeNamed(responsePointer.Elem(), "net/http", "Response") {
		return false
	}
	return typeNamed(signature.Results().At(1).Type(), "builtin", "error")
}

func roundTripReceiverName(function *types.Func) string {
	signature, ok := function.Type().(*types.Signature)
	if !ok || signature.Recv() == nil {
		return ""
	}
	receiver := signature.Recv().Type()
	if pointer, ok := receiver.(*types.Pointer); ok {
		receiver = pointer.Elem()
	}
	named, ok := receiver.(*types.Named)
	if !ok || named.Obj() == nil {
		return receiver.String()
	}
	return named.Obj().Name()
}

func productionRoundTripAllowed(root, filename, receiver string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, filename)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	allowed := map[string]map[string]bool{
		"internal/netguard/transport.go": {
			"deadlineTransport":   true,
			"contextualTransport": true,
			"limitedTransport":    true,
		},
		"internal/parser/native/http_client.go": {
			"budgetRoundTripper":   true,
			"disabledRoundTripper": true,
		},
		"internal/parser/result.go": {
			"candidateBudgetRoundTripper": true,
		},
	}
	return allowed[rel][receiver]
}

func typeNamed(value types.Type, pkgPath, name string) bool {
	named, ok := value.(*types.Named)
	if !ok || named.Obj() == nil {
		return false
	}
	if named.Obj().Name() != name {
		return false
	}
	pkg := named.Obj().Pkg()
	if pkg == nil {
		return pkgPath == "builtin"
	}
	return pkg.Path() == pkgPath
}

func productionEgressPrimitiveAllowed(root, filename string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, filename)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	switch rel {
	case "internal/netguard/validator.go",
		"internal/netguard/transport.go",
		"internal/netguard/proxy.go",
		"internal/parser/native/http_client.go",
		"internal/parser/ytdlp/runner.go":
		return true
	default:
		return false
	}
}

func forbiddenProductionFunctionAliases(file *ast.File, info *types.Info) map[types.Object]string {
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
			label, forbidden := forbiddenProductionFunctionExpression(current.value, info, aliases)
			if !forbidden {
				continue
			}
			aliases[current.object] = label
			changed = true
		}
	}
	return aliases
}

func forbiddenProductionFunctionExpression(expression ast.Expr, info *types.Info, aliases map[types.Object]string) (string, bool) {
	switch typed := unparenProductionExpression(expression).(type) {
	case *ast.SelectorExpr:
		return directForbiddenProductionFunction(typed, info)
	case *ast.Ident:
		if label, forbidden := directForbiddenProductionFunctionObject(info.Uses[typed]); forbidden {
			return label, true
		}
		label, forbidden := aliases[info.Uses[typed]]
		return label, forbidden
	default:
		return "", false
	}
}

func directForbiddenProductionFunction(selector *ast.SelectorExpr, info *types.Info) (string, bool) {
	return directForbiddenProductionFunctionObject(info.Uses[selector.Sel])
}

func directForbiddenProductionFunctionObject(object types.Object) (string, bool) {
	function, ok := object.(*types.Func)
	if !ok || function.Pkg() == nil {
		return "", false
	}
	signature, _ := function.Type().(*types.Signature)
	packageFunction := signature == nil || signature.Recv() == nil
	if !forbiddenProductionFunction(function.Pkg().Path(), function.Name(), packageFunction) {
		return "", false
	}
	return function.Pkg().Path() + "." + function.Name(), true
}

func forbiddenProductionFunction(path, name string, packageFunction bool) bool {
	switch path {
	case "net":
		return strings.HasPrefix(name, "Dial")
	case "net/http":
		return packageFunction && (name == "Get" || name == "Post" || name == "PostForm" || name == "Head")
	case "os/exec":
		return name == "Command" || name == "CommandContext"
	case "github.com/go-resty/resty/v2":
		return packageFunction && (name == "New" || name == "NewWithClient")
	default:
		return false
	}
}

func forbiddenProductionVariableObject(object types.Object) (string, bool) {
	variable, ok := object.(*types.Var)
	if !ok || variable.Pkg() == nil || variable.Pkg().Path() != "net/http" {
		return "", false
	}
	if variable.Name() != "DefaultClient" && variable.Name() != "DefaultTransport" {
		return "", false
	}
	return variable.Pkg().Path() + "." + variable.Name(), true
}

func forbiddenProductionComposite(value types.Type) bool {
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

func insecureTLSComposite(literal *ast.CompositeLit, info *types.Info) bool {
	named, ok := info.TypeOf(literal).(*types.Named)
	if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != "crypto/tls" || named.Obj().Name() != "Config" {
		return false
	}
	for _, element := range literal.Elts {
		keyValue, ok := element.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := keyValue.Key.(*ast.Ident)
		if !ok || key.Name != "InsecureSkipVerify" {
			continue
		}
		if ident, ok := keyValue.Value.(*ast.Ident); ok && ident.Name == "true" {
			return true
		}
	}
	return false
}

func unparenProductionExpression(expression ast.Expr) ast.Expr {
	for {
		parenthesized, ok := expression.(*ast.ParenExpr)
		if !ok {
			return expression
		}
		expression = parenthesized.X
	}
}
