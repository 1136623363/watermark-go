package policy_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestEveryProductionParserRejectsEmbeddedCookieHeaders(t *testing.T) {
	root := filepath.Join(repositoryRoot(t), "internal", "parser")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		unsafe, err := scanParserCookiePolicy(source)
		if err != nil {
			return err
		}
		if unsafe {
			t.Fatal("production parser source contains a Cookie header literal or restoration instruction")
		}
		return nil
	})
	if err != nil {
		t.Fatal("inspect production parser Cookie policy")
	}
}

func TestParserCookiePolicyCoversAssignmentsCallsConcatenationAndAliases(t *testing.T) {
	material := "embedded-" + "cookie-material"
	fixtures := []string{
		"package p\nvar headers = map[string]string{HttpHeaderCookie: \"" + material + "\"}\n",
		"package p\nfunc f(){ headers[HttpHeaderCookie] = \"" + material + "\" }\n",
		"package p\nfunc f(){ req.SetHeader(HttpHeaderCookie, \"embedded-\" + \"cookie-material\") }\n",
		"package p\nfunc f(){ req.Header.Set(\"Cookie\", \"" + material + "\") }\n",
		"package p\nfunc f(){ req.Header.Add(\"Cookie\", \"" + material + "\") }\n",
		"package p\nconst headerName = \"Cookie\"\nconst headerValue = \"embedded-\" + \"cookie-material\"\nfunc f(){ req.Header.Add(headerName, headerValue) }\n",
		"package p\nfunc f(){ const headerName = \"Cookie\"; const headerValue = \"embedded-\" + \"cookie-material\"; req.Header.Add(headerName, headerValue) }\n",
		"package p\nfunc f(){ cookie := \"embedded-\" + \"cookie-material\"; headers[HttpHeaderCookie] = cookie }\n",
		"package p\nfunc f(){ headerName := \"Cookie\"; value := \"embedded-\" + \"cookie-material\"; req.Header.Set(headerName, value) }\n",
		"package p\nfunc f(){ headerName := \"safe\"; headerName = \"Cookie\"; value := \"embedded-\" + \"cookie-material\"; req.Header.Set(headerName, value) }\n",
		"package p\nfunc dynamic() string{return \"\"}; func f(){ headerName := \"Cookie\"; value := \"embedded-\" + \"cookie-material\"; req.Header.Set(headerName, value); headerName = dynamic() }\n",
		"package p\nfunc f(){ headers[\"Cookie\"] = []string{\"embedded-\" + \"cookie-material\"} }\n",
		"package p\nfunc f(){ headers[HttpHeaderCookie] = string(\"embedded-\" + \"cookie-material\") }\n",
	}
	for index, source := range fixtures {
		violation, err := scanParserCookiePolicy([]byte(source))
		if err != nil {
			t.Fatalf("scan parser Cookie syntax fixture %d", index)
		}
		if !violation {
			t.Fatalf("parser Cookie syntax fixture %d bypassed the policy", index)
		}
	}
	allowed := "package p\nfunc f(configured string){ headers[HttpHeaderCookie] = configured }\n"
	if violation, err := scanParserCookiePolicy([]byte(allowed)); err != nil || violation {
		t.Fatal("parser Cookie policy rejected typed runtime injection")
	}
	shadowed := "package p\nconst headerName = \"Cookie\"\nfunc dynamic() string { return \"\" }\nfunc f(){ headerName := dynamic(); req.Header.Set(headerName, dynamic()) }\n"
	if violation, err := scanParserCookiePolicy([]byte(shadowed)); err != nil || violation {
		t.Fatal("parser Cookie policy confused a shadowed local with a package alias")
	}
}

func TestRepositoryAuditScansParserCookiePolicyInIndexWhenWorktreeIsSafe(t *testing.T) {
	for _, root := range []string{"internal/parser/native", "internal/parsers/native"} {
		root := root
		t.Run(strings.ReplaceAll(root, "/", "-"), func(t *testing.T) {
			repo := newAuditRepository(t)
			directory := filepath.Join(repo, filepath.FromSlash(root))
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal("create parser fixture directory")
			}
			path := filepath.Join(directory, "fixture.go")
			material := "embedded-" + "cookie-material"
			writeAuditFixture(t, path, "package native\nfunc f(){ headers[HttpHeaderCookie] = \""+material+"\" }\n")
			gitTestOutput(t, repo, "add", filepath.ToSlash(filepath.Join(root, "fixture.go")))
			writeAuditFixture(t, path, "package native\n")

			audit, err := auditGitRepository(repo)
			if err != nil {
				t.Fatal("audit parser Cookie policy")
			}
			if !auditHasKind(audit, "parser-cookie-literal") {
				t.Fatal("staged parser Cookie literal was hidden by a safe worktree file")
			}
		})
	}
}

func scanParserCookiePolicy(source []byte) (bool, error) {
	file, err := parser.ParseFile(token.NewFileSet(), "parser.go", source, parser.ParseComments)
	if err != nil {
		return false, err
	}
	aliases := make(map[*ast.Object][]ast.Expr)
	ast.Inspect(file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.GenDecl:
			if typed.Tok != token.CONST && typed.Tok != token.VAR {
				return true
			}
			for _, specification := range typed.Specs {
				values, ok := specification.(*ast.ValueSpec)
				if !ok || len(values.Values) == 0 {
					continue
				}
				for index, name := range values.Names {
					valueIndex := index
					if valueIndex >= len(values.Values) {
						valueIndex = len(values.Values) - 1
					}
					if name.Obj != nil {
						aliases[name.Obj] = append(aliases[name.Obj], values.Values[valueIndex])
					}
				}
			}
		case *ast.AssignStmt:
			if (typed.Tok != token.DEFINE && typed.Tok != token.ASSIGN) || len(typed.Rhs) == 0 {
				return true
			}
			for index, left := range typed.Lhs {
				name, ok := left.(*ast.Ident)
				if !ok || name.Obj == nil {
					continue
				}
				rightIndex := index
				if rightIndex >= len(typed.Rhs) {
					rightIndex = len(typed.Rhs) - 1
				}
				aliases[name.Obj] = append(aliases[name.Obj], typed.Rhs[rightIndex])
			}
		}
		return true
	})

	unsafe := false
	ast.Inspect(file, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.KeyValueExpr:
			unsafe = unsafe || (isCookieHeaderExpression(typed.Key, aliases) && isLiteralDerivedString(typed.Value, aliases))
		case *ast.CallExpr:
			if selector, ok := typed.Fun.(*ast.SelectorExpr); ok &&
				(selector.Sel.Name == "SetHeader" || selector.Sel.Name == "Set" || selector.Sel.Name == "Add") && len(typed.Args) >= 2 {
				unsafe = unsafe || (isCookieHeaderExpression(typed.Args[0], aliases) && isLiteralDerivedString(typed.Args[1], aliases))
			}
		case *ast.AssignStmt:
			for index, left := range typed.Lhs {
				indexed, ok := left.(*ast.IndexExpr)
				if !ok || !isCookieHeaderExpression(indexed.Index, aliases) || len(typed.Rhs) == 0 {
					continue
				}
				rightIndex := index
				if rightIndex >= len(typed.Rhs) {
					rightIndex = len(typed.Rhs) - 1
				}
				unsafe = unsafe || isLiteralDerivedString(typed.Rhs[rightIndex], aliases)
			}
		}
		return !unsafe
	})
	for _, group := range file.Comments {
		comment := strings.ToLower(group.Text())
		if strings.Contains(comment, "cookie") &&
			(strings.Contains(comment, "const") || strings.Contains(comment, "header.set") ||
				strings.Contains(comment, "注释") || strings.Contains(comment, "添加")) {
			unsafe = true
		}
	}
	return unsafe, nil
}

func isCookieHeaderExpression(expression ast.Expr, aliases map[*ast.Object][]ast.Expr) bool {
	switch typed := expression.(type) {
	case *ast.Ident:
		if typed.Name == "HttpHeaderCookie" {
			return true
		}
	default:
	}
	for _, value := range resolveParserStrings(expression, aliases, make(map[*ast.Object]bool)) {
		if strings.EqualFold(strings.TrimSpace(value), "cookie") {
			return true
		}
	}
	return false
}

func isLiteralDerivedString(expression ast.Expr, aliases map[*ast.Object][]ast.Expr) bool {
	for _, value := range resolveParserStrings(expression, aliases, make(map[*ast.Object]bool)) {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func resolveParserStrings(expression ast.Expr, aliases map[*ast.Object][]ast.Expr, resolving map[*ast.Object]bool) []string {
	switch typed := expression.(type) {
	case *ast.BasicLit:
		if typed.Kind != token.STRING {
			return nil
		}
		value, err := strconv.Unquote(typed.Value)
		if err != nil {
			return nil
		}
		return []string{value}
	case *ast.BinaryExpr:
		if typed.Op != token.ADD {
			return nil
		}
		left := resolveParserStrings(typed.X, aliases, resolving)
		right := resolveParserStrings(typed.Y, aliases, resolving)
		result := make([]string, 0, len(left)*len(right))
		for _, leftValue := range left {
			for _, rightValue := range right {
				result = append(result, leftValue+rightValue)
			}
		}
		return result
	case *ast.ParenExpr:
		return resolveParserStrings(typed.X, aliases, resolving)
	case *ast.CompositeLit:
		var result []string
		for _, element := range typed.Elts {
			if pair, ok := element.(*ast.KeyValueExpr); ok {
				element = pair.Value
			}
			result = append(result, resolveParserStrings(element, aliases, resolving)...)
		}
		return result
	case *ast.CallExpr:
		name, ok := typed.Fun.(*ast.Ident)
		if !ok || name.Name != "string" || len(typed.Args) != 1 {
			return nil
		}
		return resolveParserStrings(typed.Args[0], aliases, resolving)
	case *ast.Ident:
		if typed.Obj == nil || resolving[typed.Obj] {
			return nil
		}
		values := aliases[typed.Obj]
		if len(values) == 0 {
			return nil
		}
		resolving[typed.Obj] = true
		defer delete(resolving, typed.Obj)
		var result []string
		for _, value := range values {
			result = append(result, resolveParserStrings(value, aliases, resolving)...)
		}
		return result
	default:
		return nil
	}
}
