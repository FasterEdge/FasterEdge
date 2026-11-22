// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package main

// 契约提取器：从 ability/ 与 data/ 的 Go 源码静态还原"命令契约"——
// 每个组件 Command() 里 switch act 的每条分派分支、该分支对 args 的类型断言
// (args.(XxxArgs))、以及参数结构体的字段（名字 / 类型 / json tag / 是否被
// 分派分支显式拒绝零值）。
//
// 为什么不是反射/运行时探测: 组件方法签名是 Command(*types.Atom, string, any)，
// args 是 any——运行时只能试错，无法枚举"哪些字段必填"。静态 AST 是唯一能把
// "分派分支 -> 参数结构体" 这条因果链完整取出来的地方，也让结果可复现
// (同一份源码两次运行字节相同)。
//
// 已知局限(刻意显式标注而不是猜): 字段"必填"在 Go 端没有元数据，
// 只有"分派分支里对被拒零值的显式比较"。因此 Field.Required 取三态:
// required(找到空值/零值拒绝) / optional(带 omitempty 或注释写"可选") /
// unknown(静态无法判定)。unknown 不参与"必填不一致"判定。

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	argsKindNone    = "none"
	argsKindStruct  = "struct"
	argsKindUnknown = "unknown"

	reqRequired = "required"
	reqOptional = "optional"
	reqUnknown  = "unknown"

	maxDelegateDepth = 4
)

// Contract 是提取结果，也是 Simulator 侧比对器的输入。
type Contract struct {
	SchemaVersion   int              `json:"schemaVersion"`
	Generator       string           `json:"generator"`
	Stats           Stats            `json:"stats"`
	Components      []Component      `json:"components"`
	UnusedConstants []UnusedConstant `json:"unusedConstants"`
}

// Stats 汇总可被独立核对的量与"无法静态解析"的量，避免把盲区藏在结果里。
type Stats struct {
	Packages            []string `json:"packages"`
	Components          int      `json:"components"`
	Commands            int      `json:"commands"`
	CommandConstants    int      `json:"commandConstants"`
	DistinctCommandVals int      `json:"distinctCommandValues"`
	ArgsStruct          int      `json:"argsStruct"`
	ArgsNone            int      `json:"argsNone"`
	ArgsUnknown         int      `json:"argsUnknown"`
	FieldsRequired      int      `json:"fieldsRequired"`
	FieldsOptional      int      `json:"fieldsOptional"`
	FieldsUnknown       int      `json:"fieldsUnknown"`
}

// Component 是一个可被 Atom 挂载的组件（Data 或 Ability）。
type Component struct {
	Name     string    `json:"name"`
	Kind     string    `json:"kind"`
	Source   string    `json:"source"`
	Commands []Command `json:"commands"`
}

// Command 对应一条分派分支。
type Command struct {
	Name     string   `json:"name"`
	Constant string   `json:"constant"`
	Args     ArgsSpec `json:"args"`
}

// ArgsSpec 描述该分支接受的 args。
type ArgsSpec struct {
	Kind    string  `json:"kind"`
	Type    string  `json:"type,omitempty"`
	Via     string  `json:"via,omitempty"`
	Wrapper string  `json:"wrapper,omitempty"`
	Fields  []Field `json:"fields,omitempty"`
}

// Field 是参数结构体的一个字段（嵌入字段已展平并标 embedded）。
type Field struct {
	Name      string `json:"name"`
	JSON      string `json:"json"`
	Tag       string `json:"tag,omitempty"`
	Type      string `json:"type"`
	Required  string `json:"required"`
	OmitEmpty bool   `json:"omitEmpty,omitempty"`
	Embedded  bool   `json:"embedded,omitempty"`
	Doc       string `json:"doc,omitempty"`
}

// UnusedConstant 是名字带 Command、但没有任何分派分支引用的字符串常量。
// 它通常是"文档宣称存在、实际调不通"的命令，必须显式列出。
type UnusedConstant struct {
	Component string `json:"component"`
	Constant  string `json:"constant"`
	Value     string `json:"value"`
}

type structDecl struct {
	st   *ast.StructType
	file string
}

type goPkg struct {
	kind      string
	name      string
	dir       string
	fset      *token.FileSet
	files     []*ast.File
	src       map[string][]byte
	constExpr map[string]ast.Expr
	constFile map[string]string
	consts    map[string]string
	structs   map[string]*structDecl
	aliases   map[string]ast.Expr
	methods   map[string]*ast.FuncDecl
	funcs     map[string]*ast.FuncDecl
	funcFile  map[string]string
	usedConst map[string]bool
}

func loadPkg(kind, dir string) (*goPkg, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	p := &goPkg{
		kind:      kind,
		dir:       dir,
		fset:      token.NewFileSet(),
		src:       map[string][]byte{},
		constExpr: map[string]ast.Expr{},
		constFile: map[string]string{},
		consts:    map[string]string{},
		structs:   map[string]*structDecl{},
		aliases:   map[string]ast.Expr{},
		methods:   map[string]*ast.FuncDecl{},
		funcs:     map[string]*ast.FuncDecl{},
		funcFile:  map[string]string{},
		usedConst: map[string]bool{},
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		path := filepath.Join(dir, n)
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		f, err := parser.ParseFile(p.fset, path, src, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if p.name == "" {
			p.name = f.Name.Name
		}
		p.files = append(p.files, f)
		p.src[path] = src
	}
	p.collect()
	return p, nil
}

func (p *goPkg) collect() {
	for _, f := range p.files {
		file := p.fset.Position(f.Pos()).Filename
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.ValueSpec:
						for i, id := range s.Names {
							var expr ast.Expr
							switch {
							case i < len(s.Values):
								expr = s.Values[i]
							case len(s.Values) == 1:
								expr = s.Values[0]
							}
							if expr == nil {
								continue
							}
							p.constExpr[id.Name] = expr
							p.constFile[id.Name] = file
						}
					case *ast.TypeSpec:
						if st, ok := s.Type.(*ast.StructType); ok {
							p.structs[s.Name.Name] = &structDecl{st: st, file: file}
						} else {
							p.aliases[s.Name.Name] = s.Type
						}
					}
				}
			case *ast.FuncDecl:
				key := d.Name.Name
				if d.Recv != nil && len(d.Recv.List) > 0 {
					key = recvTypeName(d.Recv.List[0].Type) + "." + d.Name.Name
					p.methods[key] = d
				} else {
					p.funcs[d.Name.Name] = d
				}
				p.funcFile[key] = file
			}
		}
	}
}

func recvTypeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return recvTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return recvTypeName(t.X)
	case *ast.IndexListExpr:
		return recvTypeName(t.X)
	}
	return ""
}

// resolveConst 解析常量标识符的字符串值（支持多级别名）。
func (p *goPkg) resolveConst(name string, depth int) (string, bool) {
	if v, ok := p.consts[name]; ok {
		return v, true
	}
	if depth > 8 {
		return "", false
	}
	expr, ok := p.constExpr[name]
	if !ok {
		return "", false
	}
	var val string
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		if err != nil {
			return "", false
		}
		val = s
	case *ast.Ident:
		s, ok := p.resolveConst(e.Name, depth+1)
		if !ok {
			return "", false
		}
		val = s
	case *ast.CallExpr:
		// string(SomeConst) 形式
		if len(e.Args) == 1 {
			if id, ok := e.Args[0].(*ast.Ident); ok {
				s, ok := p.resolveConst(id.Name, depth+1)
				if !ok {
					return "", false
				}
				val = s
			}
		}
	}
	if val == "" && expr == nil {
		return "", false
	}
	p.consts[name] = val
	return val, true
}

func (p *goPkg) text(n ast.Node) string {
	if n == nil {
		return ""
	}
	pos := p.fset.Position(n.Pos())
	end := p.fset.Position(n.End())
	src, ok := p.src[filepath.Clean(pos.Filename)]
	if !ok {
		for k, v := range p.src {
			if filepath.Base(k) == filepath.Base(pos.Filename) {
				src, ok = v, true
				break
			}
		}
	}
	if !ok || pos.Offset < 0 || end.Offset > len(src) || pos.Offset > end.Offset {
		return ""
	}
	return string(src[pos.Offset:end.Offset])
}

func typeString(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + typeString(t.X)
	case *ast.SelectorExpr:
		return typeString(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + typeString(t.Elt)
		}
		return "[...]" + typeString(t.Elt)
	case *ast.MapType:
		return "map[" + typeString(t.Key) + "]" + typeString(t.Value)
	case *ast.InterfaceType:
		return "any"
	case *ast.StructType:
		parts := make([]string, 0, len(t.Fields.List))
		for _, f := range t.Fields.List {
			names := make([]string, 0, len(f.Names))
			for _, n := range f.Names {
				names = append(names, n.Name)
			}
			parts = append(parts, strings.Join(names, ",")+" "+typeString(f.Type))
		}
		return "struct{" + strings.Join(parts, "; ") + "}"
	case *ast.IndexExpr:
		return typeString(t.X) + "[" + typeString(t.Index) + "]"
	}
	return "any"
}

func paramNames(fn *ast.FuncDecl) []string { return paramListNames(fn.Type.Params) }

func paramListNames(fl *ast.FieldList) []string {
	if fl == nil {
		return nil
	}
	out := make([]string, 0, 4)
	for _, f := range fl.List {
		if len(f.Names) == 0 {
			out = append(out, "")
			continue
		}
		for _, n := range f.Names {
			out = append(out, n.Name)
		}
	}
	return out
}

// receiverInfo 返回 Command 方法的接收者变量名与类型名。
func receiverInfo(fn *ast.FuncDecl) (string, string) {
	if fn == nil || fn.Recv == nil || len(fn.Recv.List) == 0 {
		return "", ""
	}
	f := fn.Recv.List[0]
	name := ""
	if len(f.Names) > 0 {
		name = f.Names[0].Name
	}
	return name, recvTypeName(f.Type)
}

func findActSwitch(body *ast.BlockStmt) *ast.SwitchStmt {
	if body == nil {
		return nil
	}
	var found *ast.SwitchStmt
	ast.Inspect(body, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		sw, ok := n.(*ast.SwitchStmt)
		if !ok || sw.Tag == nil {
			return true
		}
		if _, ok := sw.Tag.(*ast.Ident); !ok {
			return true
		}
		found = sw
		return false
	})
	return found
}

// components 枚举本包里所有实现了 Command 且能解析出 GetName 的具体组件。
func (p *goPkg) components() ([]Component, map[string]bool) {
	out := make([]Component, 0, 16)
	skipped := map[string]bool{}
	models := make([]string, 0, len(p.methods))
	for key := range p.methods {
		if strings.HasSuffix(key, ".Command") {
			models = append(models, strings.TrimSuffix(key, ".Command"))
		}
	}
	sort.Strings(models)
	for _, recv := range models {
		name, ok := p.componentName(recv)
		if !ok {
			skipped[recv] = true
			continue
		}
		cmds := p.commandSpecs(recv)
		if len(cmds) == 0 {
			skipped[recv] = true
			continue
		}
		file := p.funcFile[recv+".Command"]
		out = append(out, Component{
			Name:     name,
			Kind:     p.kind,
			Source:   p.relSource(file),
			Commands: cmds,
		})
	}
	return out, skipped
}

func (p *goPkg) relSource(file string) string {
	base := filepath.Base(filepath.Dir(filepath.Dir(file)))
	return filepath.ToSlash(filepath.Join(base, filepath.Base(file)))
}

func (p *goPkg) componentName(recv string) (string, bool) {
	fn, ok := p.methods[recv+".GetName"]
	if !ok {
		return "", false
	}
	var name string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if name != "" {
			return false
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return true
		}
		// GetName 既可能直接返回字面量，也可能返回常量
		// (如 OneKeyAbility → oneKeyAbilityName)，两种都要解析。
		switch v := ret.Results[0].(type) {
		case *ast.BasicLit:
			if v.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(v.Value)
			if err == nil {
				name = s
			}
		case *ast.Ident:
			if s, ok := p.resolveConst(v.Name, 0); ok {
				name = s
			}
		}
		return false
	})
	return name, name != ""
}

func (p *goPkg) commandSpecs(recv string) []Command {
	fn, ok := p.methods[recv+".Command"]
	if !ok {
		return nil
	}
	params := paramNames(fn)
	if len(params) < 3 || params[2] == "" {
		return nil
	}
	actParam, argsParam := params[1], params[2]
	recvVar, recvType := receiverInfo(fn)
	ctx := resolveCtx{args: argsParam, recvVar: recvVar, recvType: recvType}
	sw := findActSwitch(fn.Body)
	if sw == nil || !isIdent(sw.Tag, actParam) {
		// 组件自己不分派：整体转发给共享实现（MySQLData/PostgreSQLData →
		// sqlCommand(&d.store, act, args)）。顺着同一条 act/args 链路继续，
		// 把目标函数的分支展开成本组件的命令，否则这两个组件会从契约里
		// 整体消失（组件数缺 2，命令缺 6）。
		return p.delegatedCommands(ctx, fn.Body)
	}
	out := make([]Command, 0, 16)
	for _, stmt := range sw.Body.List {
		clause, ok := stmt.(*ast.CaseClause)
		if !ok || clause.List == nil {
			continue
		}
		body := &ast.BlockStmt{List: clause.Body}
		for _, expr := range clause.List {
			constName, act, ok := p.caseValue(expr)
			if !ok {
				continue
			}
			p.usedConst[constName] = true
			c := ctx
			c.act = act
			c.depth = 0
			out = append(out, Command{
				Name:     act,
				Constant: constName,
				Args:     p.resolveArgs(c, body),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func (p *goPkg) caseValue(expr ast.Expr) (constName, value string, ok bool) {
	switch e := expr.(type) {
	case *ast.Ident:
		v, ok := p.resolveConst(e.Name, 0)
		if !ok {
			return e.Name, "", false
		}
		return e.Name, v, true
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", "", false
		}
		s, err := strconv.Unquote(e.Value)
		if err != nil {
			return "", "", false
		}
		return e.Value, s, true
	}
	return "", "", false
}

type resolveCtx struct {
	act      string
	args     string
	recvVar  string
	recvType string
	depth    int
	via      []string
}

func (c resolveCtx) next() resolveCtx {
	n := c
	n.depth++
	return n
}

func (c resolveCtx) viaString() string { return strings.Join(c.via, " -> ") }

type assertInfo struct {
	expr    ast.Expr
	varName string
}

func (p *goPkg) resolveArgs(c resolveCtx, body *ast.BlockStmt) ArgsSpec {
	var asserts []assertInfo
	nilOnly := false
	var delegation *ast.CallExpr

	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil {
			return true
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		switch node := n.(type) {
		case *ast.TypeAssertExpr:
			// 独立 TypeAssertExpr 出现在 if-init / 单语句 for-init 等处,
			// 断言变量名拿不到——此时 required 判定会降级为 unknown,
			// 由 Field.Required 的三态显式表达。
			return true
		case *ast.AssignStmt:
			for i, rhs := range node.Rhs {
				ta, ok := rhs.(*ast.TypeAssertExpr)
				if !ok || !isIdent(ta.X, c.args) || ta.Type == nil {
					continue
				}
				name := ""
				if i < len(node.Lhs) {
					if id, ok := node.Lhs[i].(*ast.Ident); ok {
						name = id.Name
					}
				}
				asserts = append(asserts, assertInfo{expr: ta.Type, varName: name})
			}
			return true
		case *ast.IfStmt:
			if cond, ok := node.Cond.(*ast.BinaryExpr); ok && cond.Op == token.NEQ {
				if isIdent(cond.X, c.args) && isNilIdent(cond.Y) && returnsOnly(node.Body) {
					nilOnly = true
				}
			}
			return true
		case *ast.CallExpr:
			if delegation == nil && containsIdent(node.Args, c.args) {
				delegation = node
			}
			return true
		}
		return true
	})

	if len(asserts) > 0 {
		// 同名断言可能重复出现(如 helper 内多处)，取首个即可。
		return p.structSpec(c, asserts[0], body)
	}
	if nilOnly {
		return ArgsSpec{Kind: argsKindNone, Via: c.viaString()}
	}
	if delegation != nil && c.depth < maxDelegateDepth {
		return p.delegate(c, delegation)
	}
	return ArgsSpec{Kind: argsKindUnknown, Via: c.viaString()}
}

func isNilIdent(e ast.Expr) bool { return isIdent(e, "nil") }

func containsIdent(list []ast.Expr, name string) bool {
	for _, e := range list {
		if isIdent(e, name) {
			return true
		}
	}
	return false
}

func returnsOnly(body *ast.BlockStmt) bool {
	if body == nil || len(body.List) == 0 {
		return false
	}
	for _, stmt := range body.List {
		if _, ok := stmt.(*ast.ReturnStmt); !ok {
			return false
		}
	}
	return true
}

// delegatedCommands 展开"整体转发型"组件的命令：Command 自身没有 act switch，
// 而是把 (act, args) 原样交给共享实现。返回的每条命令都带上 via 记录链路。
func (p *goPkg) delegatedCommands(c resolveCtx, body *ast.BlockStmt) []Command {
	var call *ast.CallExpr
	ast.Inspect(body, func(n ast.Node) bool {
		if n == nil || call != nil {
			return call == nil
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		if ce, ok := n.(*ast.CallExpr); ok && containsIdent(ce.Args, c.args) {
			call = ce
			return false
		}
		return true
	})
	if call == nil || c.depth >= maxDelegateDepth {
		return nil
	}
	next := c.next()
	next.via = append(append([]string{}, c.via...), p.text(call.Fun))

	var target *ast.FuncDecl
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		target = p.funcs[fun.Name]
	case *ast.SelectorExpr:
		if recvType := p.typeOfExpr(c, fun.X); recvType != "" {
			target = p.methods[recvType+"."+fun.Sel.Name]
		}
	}
	if target == nil {
		return nil
	}
	params := paramNames(target)
	for i, prm := range params {
		if prm == c.args && i < len(params) {
			next.args = prm
		}
	}
	sw := findActSwitch(target.Body)
	if sw == nil {
		return nil
	}
	out := make([]Command, 0, 16)
	for _, stmt := range sw.Body.List {
		clause, ok := stmt.(*ast.CaseClause)
		if !ok || clause.List == nil {
			continue
		}
		body := &ast.BlockStmt{List: clause.Body}
		for _, expr := range clause.List {
			constName, act, ok := p.caseValue(expr)
			if !ok {
				continue
			}
			p.usedConst[constName] = true
			branch := next
			branch.act = act
			out = append(out, Command{
				Name:     act,
				Constant: constName,
				Args:     p.resolveArgs(branch, body),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// delegate 顺着 `helper(act, args)` / `x.Method(act, args)` 继续解析参数结构体。
func (p *goPkg) delegate(c resolveCtx, call *ast.CallExpr) ArgsSpec {
	via := append(append([]string{}, c.via...), p.text(call.Fun))
	next := c.next()
	next.via = via

	var target *ast.FuncDecl
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		target = p.funcs[fun.Name]
	case *ast.SelectorExpr:
		if recvType := p.typeOfExpr(c, fun.X); recvType != "" {
			target = p.methods[recvType+"."+fun.Sel.Name]
		}
	}
	if target == nil {
		return ArgsSpec{Kind: argsKindUnknown, Via: strings.Join(via, " -> ")}
	}

	// 目标函数里的 act 表达式：act 原样透传、常量、或 mapX(act) 映射。
	targetAct := c.act
	for _, arg := range call.Args {
		if isIdent(arg, c.args) {
			break
		}
		if v, ok := p.actExpr(c, arg); ok {
			targetAct = v
		}
	}

	params := paramNames(target)
	argsIdx := -1
	for i, prm := range params {
		if prm == c.args {
			argsIdx = i
		}
	}
	targetArgs := c.args
	if argsIdx >= 0 && argsIdx < len(params) {
		targetArgs = params[argsIdx]
	}
	next.act = targetAct
	next.args = targetArgs
	if rv, rt := receiverInfo(target); rt != "" {
		next.recvVar, next.recvType = rv, rt
	}

	if sw := findActSwitch(target.Body); sw != nil {
		for _, stmt := range sw.Body.List {
			clause, ok := stmt.(*ast.CaseClause)
			if !ok || clause.List == nil {
				continue
			}
			for _, expr := range clause.List {
				if _, v, ok := p.caseValue(expr); ok && v == targetAct {
					spec := p.resolveArgs(next, &ast.BlockStmt{List: clause.Body})
					// 分支内没有再断言时，退回 helper 整体(顶部常有的统一断言)。
					if spec.Kind == argsKindUnknown {
						spec = p.resolveArgs(next, target.Body)
					}
					spec.Via = strings.Join(via, " -> ")
					return spec
				}
			}
		}
	}
	spec := p.resolveArgs(next, target.Body)
	spec.Via = strings.Join(via, " -> ")
	return spec
}

// actExpr 解析调用点上的 act 实参：act 本身 / 常量 / mapFn(act)。
func (p *goPkg) actExpr(c resolveCtx, e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.Ident:
		if v.Name == c.act {
			return c.act, true
		}
		if s, ok := p.resolveConst(v.Name, 0); ok {
			return s, true
		}
	case *ast.CallExpr:
		if len(v.Args) != 1 {
			return "", false
		}
		fn, ok := p.funcs[p.text(v.Fun)]
		if !ok {
			return "", false
		}
		return p.mapViaFunc(fn, c.act)
	}
	return "", false
}

// mapViaFunc 解析 `switch act { case A: return B; ... }` 形式的 act 映射函数。
func (p *goPkg) mapViaFunc(fn *ast.FuncDecl, act string) (string, bool) {
	sw := findActSwitch(fn.Body)
	if sw == nil {
		return "", false
	}
	for _, stmt := range sw.Body.List {
		clause, ok := stmt.(*ast.CaseClause)
		if !ok || clause.List == nil {
			continue
		}
		for _, expr := range clause.List {
			if _, v, ok := p.caseValue(expr); ok && v == act {
				var out string
				ast.Inspect(&ast.BlockStmt{List: clause.Body}, func(n ast.Node) bool {
					ret, ok := n.(*ast.ReturnStmt)
					if !ok || len(ret.Results) != 1 || out != "" {
						return true
					}
					if v, ok := p.actExpr(resolveCtx{act: act}, ret.Results[0]); ok {
						out = v
					}
					return false
				})
				if out != "" {
					return out, true
				}
			}
		}
	}
	return "", false
}

// typeOfExpr 推断表达式在 pkg 内的结构体类型名（用于 x.Method(...) 的接收者）。
func (p *goPkg) typeOfExpr(c resolveCtx, e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		if v.Name == c.recvVar {
			return c.recvType
		}
		return ""
	case *ast.SelectorExpr:
		base := p.typeOfExpr(c, v.X)
		if base == "" {
			return ""
		}
		return p.fieldType(base, v.Sel.Name)
	}
	return ""
}

func (p *goPkg) fieldType(structName, fieldName string) string {
	sd, ok := p.structs[structName]
	if !ok {
		return ""
	}
	for _, f := range sd.st.Fields.List {
		for _, n := range f.Names {
			if n.Name != fieldName {
				continue
			}
			t := typeString(f.Type)
			return strings.TrimPrefix(t, "*")
		}
	}
	return ""
}

func (p *goPkg) structSpec(c resolveCtx, info assertInfo, body *ast.BlockStmt) ArgsSpec {
	bodyText := p.text(body)
	var st *ast.StructType
	typeName := ""
	switch t := info.expr.(type) {
	case *ast.StructType:
		st = t
		typeName = typeString(t)
	case *ast.Ident:
		typeName = t.Name
		st = p.lookupStruct(t.Name, 0)
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			typeName = "*" + id.Name
			st = p.lookupStruct(id.Name, 0)
		}
	default:
		typeName = typeString(info.expr)
	}
	if st == nil {
		return ArgsSpec{Kind: argsKindUnknown, Type: typeName, Via: c.viaString()}
	}
	fields := make([]Field, 0, len(st.Fields.List))
	for _, f := range st.Fields.List {
		fields = append(fields, p.fieldsOf(f, bodyText, info.varName, c)...)
	}
	spec := ArgsSpec{Kind: argsKindStruct, Type: typeName, Via: c.viaString(), Fields: fields}
	// 单个"包着一层结构体"的字段（configure 系列）标记为 wrapper：Simulator 侧
	// 用扁平参数，这与 Go 的嵌套契约是结构性差异，单独归类而不是伪装成字段名笔误。
	if len(fields) == 1 && fields[0].Type != "" && !fields[0].Embedded {
		fieldStruct := p.lookupStruct(strings.TrimPrefix(fields[0].Type, "*"), 0)
		if fieldStruct != nil {
			spec.Wrapper = fields[0].Type
		}
	}
	return spec
}

func (p *goPkg) lookupStruct(name string, depth int) *ast.StructType {
	if depth > 8 {
		return nil
	}
	if sd, ok := p.structs[name]; ok {
		return sd.st
	}
	if a, ok := p.aliases[name]; ok {
		if st, ok := a.(*ast.StructType); ok {
			return st
		}
		if e, ok := a.(ast.Expr); ok {
			if id, ok := e.(*ast.Ident); ok {
				return p.lookupStruct(id.Name, depth+1)
			}
		}
	}
	return nil
}

func (p *goPkg) fieldsOf(f *ast.Field, bodyText, argVar string, c resolveCtx) []Field {
	doc := strings.TrimSpace(strings.ReplaceAll(f.Doc.Text(), "\n", " "))
	if doc == "" {
		doc = strings.TrimSpace(strings.ReplaceAll(f.Comment.Text(), "\n", " "))
	}
	tag := ""
	jsonName := ""
	omitEmpty := false
	if f.Tag != nil {
		raw := f.Tag.Value
		if s, err := strconv.Unquote(raw); err == nil {
			tag = s
		}
		if m := jsonTagRe.FindStringSubmatch(tag); m != nil {
			parts := strings.Split(m[1], ",")
			if parts[0] != "" && parts[0] != "-" {
				jsonName = parts[0]
			}
			for _, opt := range parts[1:] {
				if opt == "omitempty" {
					omitEmpty = true
				}
			}
		}
	}
	if len(f.Names) == 0 {
		// 嵌入字段：展平，保留 embedded 标记
		embeddedType := strings.TrimPrefix(typeString(f.Type), "*")
		st := p.lookupStruct(embeddedType, 0)
		if st == nil {
			return []Field{{Name: embeddedType, JSON: embeddedType, Type: typeString(f.Type), Required: reqUnknown, Embedded: true, Doc: doc}}
		}
		out := make([]Field, 0, len(st.Fields.List))
		for _, sub := range st.Fields.List {
			subFields := p.fieldsOf(sub, bodyText, argVar, c)
			for i := range subFields {
				subFields[i].Embedded = true
			}
			out = append(out, subFields...)
		}
		return out
	}
	out := make([]Field, 0, len(f.Names))
	for _, n := range f.Names {
		name := n.Name
		if jsonName == "" {
			jsonName = name
		}
		goType := typeString(f.Type)
		req := reqUnknown
		switch {
		case omitEmpty || strings.Contains(doc, "可选") || strings.Contains(strings.ToLower(doc), "optional"):
			req = reqOptional
		case p.guardsZero(bodyText, argVar, name):
			req = reqRequired
		}
		out = append(out, Field{
			Name:      name,
			JSON:      jsonName,
			Tag:       tag,
			Type:      goType,
			Required:  req,
			OmitEmpty: omitEmpty,
			Doc:       doc,
		})
		jsonName = ""
	}
	return out
}

var jsonTagRe = regexp.MustCompile(`json:"([^"]*)"`)

// guardsZero 判断分派分支里是否有对该字段空值/零值的显式拒绝。
// 只看"比较"这一种形态：Go 端没有必填元数据，宁可标 unknown 也不猜。
func (p *goPkg) guardsZero(bodyText, argVar, field string) bool {
	if bodyText == "" {
		return false
	}
	candidates := []string{}
	if argVar != "" {
		candidates = append(candidates, argVar+"."+field)
	}
	// 局部别名：x := strings.TrimSpace(typed.F) 之后对 x 判空
	aliasRe := regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*:=\s*[^\n]*\b(` + regexp.QuoteMeta(argVar) + `\.` + regexp.QuoteMeta(field) + `)\b`)
	for _, m := range aliasRe.FindAllStringSubmatch(bodyText, -1) {
		candidates = append(candidates, m[1])
	}
	if argVar == "" {
		aliasRe2 := regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*:=\s*[^\n]*\b(` + regexp.QuoteMeta(field) + `)\b`)
		for _, m := range aliasRe2.FindAllStringSubmatch(bodyText, -1) {
			if m[1] == field {
				continue
			}
			candidates = append(candidates, m[1])
		}
		if len(candidates) == 0 {
			candidates = append(candidates, field)
		}
	}
	for _, cand := range candidates {
		q := regexp.QuoteMeta(cand)
		patterns := []string{
			`\b` + q + `\s*\)?\s*==\s*""`,
			`len\(\s*` + q + `\s*\)\s*==\s*0`,
			`\b` + q + `\s*==\s*0\b`,
			`\b` + q + `\s*==\s*nil\b`,
		}
		for _, pat := range patterns {
			if regexp.MustCompile(pat).MatchString(bodyText) {
				return true
			}
		}
	}
	return false
}

// commandConstants 列出本包里名字带 Command 的字符串常量（命令候选全集）。
func (p *goPkg) commandConstants() map[string]string {
	out := map[string]string{}
	for name := range p.constExpr {
		if !strings.Contains(name, "Command") {
			continue
		}
		if v, ok := p.resolveConst(name, 0); ok && v != "" {
			out[name] = v
		}
	}
	return out
}

// simContract 不是这里要处理的域（Simulator 侧由 Node 比对器读取），
// 保留注释说明提取器只负责 Go 一侧的"权威契约"。
func extract(repo, gen string) (*Contract, error) {
	abilityPkg, err := loadPkg("ability", filepath.Join(repo, "ability"))
	if err != nil {
		return nil, err
	}
	dataPkg, err := loadPkg("data", filepath.Join(repo, "data"))
	if err != nil {
		return nil, err
	}
	contract := &Contract{
		SchemaVersion: 1,
		Generator:     gen,
		Components:    []Component{},
		Stats:         Stats{Packages: []string{"ability", "data"}},
	}
	constValues := map[string]string{}
	constOwner := map[string]string{}
	unused := make([]UnusedConstant, 0, 8)

	for _, p := range []*goPkg{abilityPkg, dataPkg} {
		comps, skipped := p.components()
		contract.Components = append(contract.Components, comps...)
		consts := p.commandConstants()
		for name, val := range consts {
			constValues[name] = val
			constOwner[name] = p.kind
			if !p.usedConst[name] {
				unused = append(unused, UnusedConstant{Component: p.kind, Constant: name, Value: val})
			}
		}
		for recv := range skipped {
			// 抽象基类（如 types.Atom）不参与；本包内既无 GetName 也无分派分支的
			// 类型才会到这里，记入 unusedConstants 之外由 stats 体现。
			_ = recv
		}
	}

	sort.Slice(contract.Components, func(i, j int) bool {
		if contract.Components[i].Kind != contract.Components[j].Kind {
			return contract.Components[i].Kind < contract.Components[j].Kind
		}
		return contract.Components[i].Name < contract.Components[j].Name
	})
	sort.Slice(unused, func(i, j int) bool {
		if unused[i].Constant != unused[j].Constant {
			return unused[i].Constant < unused[j].Constant
		}
		return unused[i].Value < unused[j].Value
	})
	contract.UnusedConstants = unused

	seen := map[string]bool{}
	for _, comp := range contract.Components {
		contract.Stats.Components++
		for _, cmd := range comp.Commands {
			contract.Stats.Commands++
			seen[cmd.Name] = true
			switch cmd.Args.Kind {
			case argsKindStruct:
				contract.Stats.ArgsStruct++
			case argsKindNone:
				contract.Stats.ArgsNone++
			default:
				contract.Stats.ArgsUnknown++
			}
			for _, f := range cmd.Args.Fields {
				switch f.Required {
				case reqRequired:
					contract.Stats.FieldsRequired++
				case reqOptional:
					contract.Stats.FieldsOptional++
				default:
					contract.Stats.FieldsUnknown++
				}
			}
		}
	}
	contract.Stats.CommandConstants = len(constValues)
	contract.Stats.DistinctCommandVals = len(seen)
	_ = constOwner
	return contract, nil
}

// contract 侧字段名的 json 兜底：encoding/json 在没有 tag 时用 Go 字段名。
func effectiveJSON(name, tag string) string {
	if m := jsonTagRe.FindStringSubmatch(tag); m != nil {
		parts := strings.Split(m[1], ",")
		if parts[0] != "" && parts[0] != "-" {
			return parts[0]
		}
	}
	return name
}
