// Command export reads the drawing tokens and appearance palette from the
// product source. It deliberately supports only the small AST forms used by
// cmd/mimac/design.go so source changes cannot silently disappear from output.
package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const sourcePath = "cmd/mimac/design.go"

var paletteRoles = []string{
	"Background", "Text", "TextMuted", "Surface", "SurfaceHover", "SurfacePressed",
	"Border", "Accent", "AccentHover", "AccentPressed", "AccentText", "Success",
	"Warning", "Danger", "Selection", "Focus", "Inverse", "InverseText", "Scrollbar",
}

type sourceRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Path string `json:"path"`
	Line int    `json:"line"`
}

type output struct {
	Tokens   map[string]float64           `json:"tokens"`
	Palettes map[string]map[string]string `json:"palettes"`
	Sources  []sourceRef                  `json:"sources"`
}

type numericConst struct {
	expr ast.Expr
	pos  token.Pos
}

func main() {
	root := flag.String("root", "../..", "repository root (default is relative to tools/workbench/export)")
	flag.Parse()
	if flag.NArg() != 0 {
		fatalf("unexpected positional arguments: %s", strings.Join(flag.Args(), " "))
	}
	rootExplicit := false
	flag.Visit(func(value *flag.Flag) {
		if value.Name == "root" {
			rootExplicit = true
		}
	})
	absRoot, err := resolveRoot(*root, rootExplicit)
	if err != nil {
		fatalf("resolve repository root: %v", err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(absRoot, sourcePath), nil, parser.AllErrors)
	if err != nil {
		fatalf("parse %s: %v", sourcePath, err)
	}
	result, err := extract(fset, file)
	if err != nil {
		fatalf("extract %s: %v", sourcePath, err)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		fatalf("write JSON: %v", err)
	}
}

func resolveRoot(value string, explicit bool) (string, error) {
	candidate, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(candidate, sourcePath)); err == nil {
		return candidate, nil
	} else if explicit {
		return "", fmt.Errorf("%s does not contain %s", candidate, sourcePath)
	}

	// The default ../.. is convenient for the repo's workbench launch command.
	// Also find the containing checkout when go run is invoked from the repo
	// root or from tools/workbench/export itself.
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if _, statErr := os.Stat(filepath.Join(dir, sourcePath)); statErr == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return "", fmt.Errorf("default root %q and current directory ancestry do not contain %s", value, sourcePath)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func extract(fset *token.FileSet, file *ast.File) (output, error) {
	consts, err := findDrawingConsts(fset, file)
	if err != nil {
		return output{}, err
	}
	tokens, tokenSources, err := resolveNumericConsts(fset, consts)
	if err != nil {
		return output{}, err
	}
	appearance, err := findFunction(file, "appAppearance")
	if err != nil {
		return output{}, err
	}
	light, lightSources, err := extractPalette(fset, appearance, false)
	if err != nil {
		return output{}, fmt.Errorf("light palette: %w", err)
	}
	dark, darkSources, err := extractPalette(fset, appearance, true)
	if err != nil {
		return output{}, fmt.Errorf("dark palette: %w", err)
	}
	sources := append(tokenSources, lightSources...)
	sources = append(sources, darkSources...)
	sort.Slice(sources, func(i, j int) bool {
		a, b := sources[i], sources[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Line < b.Line
	})
	return output{
		Tokens: tokens,
		Palettes: map[string]map[string]string{
			"light": light,
			"dark":  dark,
		},
		Sources: sources,
	}, nil
}

func findDrawingConsts(fset *token.FileSet, file *ast.File) (map[string]numericConst, error) {
	var drawing *ast.GenDecl
	for _, decl := range file.Decls {
		group, ok := decl.(*ast.GenDecl)
		if !ok || group.Tok != token.CONST {
			continue
		}
		for _, raw := range group.Specs {
			spec := raw.(*ast.ValueSpec)
			for _, name := range spec.Names {
				if name.Name == "textCaption" {
					drawing = group
				}
			}
		}
		if drawing != nil {
			break
		}
	}
	if drawing == nil {
		return nil, errors.New("drawing token const block containing textCaption was not found")
	}
	values := make(map[string]numericConst)
	for _, raw := range drawing.Specs {
		spec := raw.(*ast.ValueSpec)
		if len(spec.Values) == 0 || len(spec.Values) != len(spec.Names) {
			return nil, fmt.Errorf("%s: drawing const declaration has unsupported implicit or multi-value form", fset.Position(spec.Pos()))
		}
		for i, name := range spec.Names {
			if _, duplicate := values[name.Name]; duplicate {
				return nil, fmt.Errorf("%s: duplicate drawing token %q", fset.Position(name.Pos()), name.Name)
			}
			values[name.Name] = numericConst{expr: spec.Values[i], pos: name.Pos()}
		}
	}
	if len(values) == 0 {
		return nil, errors.New("drawing token block is empty")
	}

	// The statusTone enum is intentionally out of scope. Any other const block
	// must remain explicit so an added drawing token cannot be missed silently.
	for _, decl := range file.Decls {
		group, ok := decl.(*ast.GenDecl)
		if !ok || group.Tok != token.CONST || group == drawing {
			continue
		}
		isToneEnum, containsIota := true, false
		for _, raw := range group.Specs {
			spec := raw.(*ast.ValueSpec)
			for _, name := range spec.Names {
				if !strings.HasPrefix(name.Name, "tone") {
					isToneEnum = false
				}
			}
			for _, expr := range spec.Values {
				ast.Inspect(expr, func(node ast.Node) bool {
					if ident, ok := node.(*ast.Ident); ok && ident.Name == "iota" {
						containsIota = true
					}
					return true
				})
			}
		}
		if isToneEnum && containsIota {
			continue
		}
		return nil, fmt.Errorf("%s: unexpected const declaration outside drawing tokens; update the exporter explicitly", fset.Position(group.Pos()))
	}
	return values, nil
}

func resolveNumericConsts(fset *token.FileSet, values map[string]numericConst) (map[string]float64, []sourceRef, error) {
	resolved := make(map[string]float64, len(values))
	visiting := make(map[string]bool, len(values))
	var resolveName func(string) (float64, error)
	var eval func(ast.Expr, string) (float64, error)
	resolveName = func(name string) (float64, error) {
		if value, ok := resolved[name]; ok {
			return value, nil
		}
		decl, ok := values[name]
		if !ok {
			return 0, fmt.Errorf("unknown numeric alias %q", name)
		}
		if visiting[name] {
			return 0, fmt.Errorf("%s: alias cycle at %q", fset.Position(decl.pos), name)
		}
		visiting[name] = true
		value, err := eval(decl.expr, name)
		delete(visiting, name)
		if err != nil {
			return 0, err
		}
		resolved[name] = value
		return value, nil
	}
	eval = func(expr ast.Expr, name string) (float64, error) {
		switch node := expr.(type) {
		case *ast.BasicLit:
			if node.Kind != token.INT && node.Kind != token.FLOAT {
				return 0, fmt.Errorf("%s: token %q uses unsupported literal %s", fset.Position(node.Pos()), name, node.Kind)
			}
			value := constant.MakeFromLiteral(node.Value, node.Kind, 0)
			if value.Kind() == constant.Unknown {
				return 0, fmt.Errorf("%s: token %q has invalid numeric literal %q", fset.Position(node.Pos()), name, node.Value)
			}
			converted, _ := constant.Float64Val(constant.ToFloat(value))
			if math.IsInf(converted, 0) || math.IsNaN(converted) {
				return 0, fmt.Errorf("%s: token %q cannot be represented as finite float64", fset.Position(node.Pos()), name)
			}
			return converted, nil
		case *ast.Ident:
			return resolveName(node.Name)
		case *ast.ParenExpr:
			return eval(node.X, name)
		case *ast.UnaryExpr:
			if node.Op != token.ADD && node.Op != token.SUB {
				return 0, fmt.Errorf("%s: token %q uses unsupported unary operator %s", fset.Position(node.Pos()), name, node.Op)
			}
			value, err := eval(node.X, name)
			if err != nil {
				return 0, err
			}
			if node.Op == token.SUB {
				value = -value
			}
			return value, nil
		default:
			return 0, fmt.Errorf("%s: token %q uses unsupported expression %T", fset.Position(expr.Pos()), name, expr)
		}
	}

	sources := make([]sourceRef, 0, len(values))
	for name, decl := range values {
		if _, err := resolveName(name); err != nil {
			return nil, nil, err
		}
		sources = append(sources, sourceRef{Kind: "token", Name: name, Path: sourcePath, Line: fset.Position(decl.pos).Line})
	}
	return resolved, sources, nil
}

func findFunction(file *ast.File, name string) (*ast.FuncDecl, error) {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name && fn.Body != nil {
			return fn, nil
		}
	}
	return nil, fmt.Errorf("function %s was not found", name)
}

type colorValue struct {
	value string
	pos   token.Pos
}

type paletteState struct {
	theme map[string]colorValue
	aux   map[string]colorValue
}

func extractPalette(fset *token.FileSet, fn *ast.FuncDecl, dark bool) (map[string]string, []sourceRef, error) {
	state := paletteState{theme: map[string]colorValue{}, aux: map[string]colorValue{}}
	if err := interpretPaletteStatements(fset, fn.Body.List, &state, dark); err != nil {
		return nil, nil, err
	}
	palette := make(map[string]string, len(paletteRoles)+2)
	refs := make([]sourceRef, 0, len(paletteRoles)+2)
	for _, role := range paletteRoles {
		value, ok := state.theme[role]
		if !ok {
			return nil, nil, fmt.Errorf("theme role %s was not assigned", role)
		}
		palette[role] = value.value
		refs = append(refs, sourceRef{Kind: paletteKind(dark), Name: role, Path: sourcePath, Line: fset.Position(value.pos).Line})
	}
	for key, role := range map[string]string{"separator": "Separator", "disabled": "Disabled"} {
		value, ok := state.aux[key]
		if !ok {
			return nil, nil, fmt.Errorf("appearance role %s was not assigned", role)
		}
		palette[role] = value.value
		refs = append(refs, sourceRef{Kind: paletteKind(dark), Name: role, Path: sourcePath, Line: fset.Position(value.pos).Line})
	}
	return palette, refs, nil
}

func paletteKind(dark bool) string {
	if dark {
		return "palette.dark"
	}
	return "palette.light"
}

func interpretPaletteStatements(fset *token.FileSet, statements []ast.Stmt, state *paletteState, dark bool) error {
	for _, statement := range statements {
		switch node := statement.(type) {
		case *ast.AssignStmt:
			if err := interpretPaletteAssignment(fset, node, state); err != nil {
				return err
			}
		case *ast.IfStmt:
			condition, ok := node.Cond.(*ast.Ident)
			if !ok || condition.Name != "dark" || node.Init != nil || node.Else != nil {
				return fmt.Errorf("%s: unsupported conditional in appAppearance", fset.Position(node.Pos()))
			}
			if dark {
				if err := interpretPaletteStatements(fset, node.Body.List, state, dark); err != nil {
					return err
				}
			}
		case *ast.ReturnStmt:
			// The returned appearance packages the values already collected.
		default:
			return fmt.Errorf("%s: unsupported statement in appAppearance: %T", fset.Position(statement.Pos()), statement)
		}
	}
	return nil
}

func interpretPaletteAssignment(fset *token.FileSet, assignment *ast.AssignStmt, state *paletteState) error {
	if assignment.Tok == token.DEFINE {
		if len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 {
			return fmt.Errorf("%s: unsupported short declaration in appAppearance", fset.Position(assignment.Pos()))
		}
		name, ok := assignment.Lhs[0].(*ast.Ident)
		if !ok {
			return fmt.Errorf("%s: unsupported short declaration target", fset.Position(assignment.Pos()))
		}
		switch name.Name {
		case "t":
			unary, ok := assignment.Rhs[0].(*ast.StarExpr)
			call, callOK := (*ast.CallExpr)(nil), false
			if ok {
				call, callOK = unary.X.(*ast.CallExpr)
			}
			if !ok || !callOK || !isSelectorCall(call, "ui", "LightTheme") || len(call.Args) != 0 {
				return fmt.Errorf("%s: unsupported theme initialization in appAppearance", fset.Position(assignment.Pos()))
			}
			return nil
		case "a":
			literal, ok := assignment.Rhs[0].(*ast.CompositeLit)
			if !ok || !isIdentType(literal.Type, "appearance") {
				return fmt.Errorf("%s: unsupported appearance initialization in appAppearance", fset.Position(assignment.Pos()))
			}
			for _, raw := range literal.Elts {
				field, ok := raw.(*ast.KeyValueExpr)
				if !ok {
					return fmt.Errorf("%s: unsupported appearance literal element", fset.Position(raw.Pos()))
				}
				key, ok := field.Key.(*ast.Ident)
				if !ok || (key.Name != "separator" && key.Name != "disabled") {
					return fmt.Errorf("%s: unsupported appearance color field", fset.Position(field.Pos()))
				}
				value, err := evalPaletteColor(fset, field.Value, *state)
				if err != nil {
					return err
				}
				state.aux[key.Name] = colorValue{value: value, pos: field.Value.Pos()}
			}
			return nil
		default:
			return fmt.Errorf("%s: unsupported local declaration %q in appAppearance", fset.Position(assignment.Pos()), name.Name)
		}
	}
	if assignment.Tok != token.ASSIGN || len(assignment.Lhs) != len(assignment.Rhs) {
		return fmt.Errorf("%s: unsupported assignment form in appAppearance", fset.Position(assignment.Pos()))
	}
	type target struct{ root, field string }
	targets := make([]target, len(assignment.Lhs))
	for i, lhs := range assignment.Lhs {
		selector, ok := lhs.(*ast.SelectorExpr)
		if !ok {
			return fmt.Errorf("%s: unsupported assignment target in appAppearance", fset.Position(lhs.Pos()))
		}
		root, ok := selector.X.(*ast.Ident)
		if !ok || (root.Name != "t" && root.Name != "a") {
			return fmt.Errorf("%s: unsupported assignment receiver in appAppearance", fset.Position(lhs.Pos()))
		}
		targets[i] = target{root: root.Name, field: selector.Sel.Name}
	}
	values := make([]string, len(assignment.Rhs))
	for i, rhs := range assignment.Rhs {
		value, err := evalPaletteColor(fset, rhs, *state)
		if err != nil {
			if isNonColorTarget(targets[i]) {
				continue
			}
			return err
		}
		values[i] = value
	}
	for i, target := range targets {
		pos := assignment.Rhs[i].Pos()
		if target.root == "t" && contains(paletteRoles, target.field) {
			state.theme[target.field] = colorValue{value: values[i], pos: pos}
			continue
		}
		if target.root == "a" && (target.field == "separator" || target.field == "disabled") {
			state.aux[target.field] = colorValue{value: values[i], pos: pos}
			continue
		}
		if isNonColorTarget(target) {
			continue
		}
		return fmt.Errorf("%s: unsupported appAppearance assignment target %s.%s", fset.Position(assignment.Lhs[i].Pos()), target.root, target.field)
	}
	return nil
}

func isNonColorTarget(target struct{ root, field string }) bool {
	return target.root == "t" && contains([]string{"Dark", "FontSize", "Radius", "Spacing"}, target.field) ||
		target.root == "a" && contains([]string{"fonts", "theme"}, target.field)
}

func evalPaletteColor(fset *token.FileSet, expr ast.Expr, state paletteState) (string, error) {
	switch node := expr.(type) {
	case *ast.CallExpr:
		if !isSelectorCall(node, "ui", "Hex") || len(node.Args) != 1 {
			return "", fmt.Errorf("%s: unsupported palette expression %T", fset.Position(expr.Pos()), expr)
		}
		literal, ok := node.Args[0].(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return "", fmt.Errorf("%s: ui.Hex requires a string literal in appAppearance", fset.Position(expr.Pos()))
		}
		value, err := strconv.Unquote(literal.Value)
		if err != nil || !validHexColor(value) {
			return "", fmt.Errorf("%s: invalid palette hex value %q", fset.Position(expr.Pos()), literal.Value)
		}
		return strings.ToLower(value), nil
	case *ast.SelectorExpr:
		root, ok := node.X.(*ast.Ident)
		if !ok {
			return "", fmt.Errorf("%s: unsupported palette alias receiver", fset.Position(expr.Pos()))
		}
		if root.Name == "t" {
			if value, ok := state.theme[node.Sel.Name]; ok {
				return value.value, nil
			}
			return "", fmt.Errorf("%s: palette alias t.%s is used before assignment", fset.Position(expr.Pos()), node.Sel.Name)
		}
		if root.Name == "a" {
			if value, ok := state.aux[node.Sel.Name]; ok {
				return value.value, nil
			}
			return "", fmt.Errorf("%s: palette alias a.%s is used before assignment", fset.Position(expr.Pos()), node.Sel.Name)
		}
		return "", fmt.Errorf("%s: unsupported palette alias receiver %q", fset.Position(expr.Pos()), root.Name)
	case *ast.ParenExpr:
		return evalPaletteColor(fset, node.X, state)
	default:
		return "", fmt.Errorf("%s: unsupported palette expression %T", fset.Position(expr.Pos()), expr)
	}
}

func validHexColor(value string) bool {
	if len(value) != 7 && len(value) != 9 || value[0] != '#' {
		return false
	}
	_, err := hex.DecodeString(value[1:])
	return err == nil
}

func isSelectorCall(call *ast.CallExpr, root, selector string) bool {
	fun, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || fun.Sel.Name != selector {
		return false
	}
	ident, ok := fun.X.(*ast.Ident)
	return ok && ident.Name == root
}

func isIdentType(expr ast.Expr, name string) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == name
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
