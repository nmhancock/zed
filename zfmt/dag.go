package zfmt

import (
	"fmt"

	"github.com/brimdata/zed/compiler/ast/dag"
	astzed "github.com/brimdata/zed/compiler/ast/zed"
	"github.com/brimdata/zed/compiler/kernel"
	"github.com/brimdata/zed/order"
)

func DAG(op dag.Op) string {
	d := &canonDAG{
		canonZed: canonZed{formatter: formatter{tab: 2}},
		head:     true,
		first:    true,
	}
	d.op(op)
	d.flush()
	return d.String()
}

func DAGExpr(e dag.Expr) string {
	d := &canonDAG{
		canonZed: canonZed{formatter: formatter{tab: 2}},
		head:     true,
		first:    true,
	}
	d.expr(e, "")
	d.flush()
	return d.String()
}

type canonDAG struct {
	canonZed
	head  bool
	first bool
}

func (c *canonDAG) open(args ...interface{}) {
	c.formatter.open(args...)
}

func (c *canonDAG) close() {
	c.formatter.close()
}

func (c *canonDAG) assignments(assignments []dag.Assignment) {
	for k, a := range assignments {
		if k > 0 {
			c.write(",")
		}
		if a.LHS != nil {
			c.expr(a.LHS, "")
			c.write(":=")
		}
		c.expr(a.RHS, "")
	}
}

func (c *canonDAG) exprs(exprs []dag.Expr) {
	for k, e := range exprs {
		if k > 0 {
			c.write(", ")
		}
		c.expr(e, "")
	}
}

func (c *canonDAG) expr(e dag.Expr, parent string) {
	switch e := e.(type) {
	case nil:
		c.write("null")
	case *dag.Agg:
		c.write("%s(", e.Name)
		if e.Expr != nil {
			c.expr(e.Expr, "")
		}
		c.write(")")
		if e.Where != nil {
			c.write(" where ")
			c.expr(e.Where, "")
		}
	case *astzed.Primitive:
		c.literal(*e)
	case *dag.UnaryExpr:
		c.write(e.Op)
		c.expr(e.Operand, "not")
	case *dag.BinaryExpr:
		c.binary(e, parent)
	case *dag.Conditional:
		c.write("(")
		c.expr(e.Cond, "")
		c.write(") ? ")
		c.expr(e.Then, "")
		c.write(" : ")
		c.expr(e.Else, "")
	case *dag.Call:
		c.write("%s(", e.Name)
		c.exprs(e.Args)
		c.write(")")
	case *dag.Search:
		c.write("search(%s)", e.Value)
	case *dag.This:
		c.fieldpath(e.Path)
	case *dag.Var:
		c.write("%s", e.Name)
	case *dag.Literal:
		c.write("%s", e.Value)
	case *astzed.TypeValue:
		c.write("type<")
		c.typ(e.Value)
		c.write(">")
	default:
		c.open("(unknown expr %T)", e)
		c.close()
		c.ret()
	}
}

func (c *canonDAG) binary(e *dag.BinaryExpr, parent string) {
	switch e.Op {
	case ".":
		if !isDAGThis(e.LHS) {
			c.expr(e.LHS, "")
			c.write(".")
		}
		c.expr(e.RHS, "")
	case "[":
		if isDAGThis(e.LHS) {
			c.write(".")
		} else {
			c.expr(e.LHS, "")
		}
		c.write("[")
		c.expr(e.RHS, "")
		c.write("]")
	case "in", "and", "or":
		parens := needsparens(parent, e.Op)
		c.maybewrite("(", parens)
		c.expr(e.LHS, e.Op)
		c.write(" %s ", e.Op)
		c.expr(e.RHS, e.Op)
		c.maybewrite(")", parens)
	default:
		parens := needsparens(parent, e.Op)
		c.maybewrite("(", parens)
		c.expr(e.LHS, e.Op)
		c.write("%s", e.Op)
		c.expr(e.RHS, e.Op)
		c.maybewrite(")", parens)
	}
}

func isDAGThis(e dag.Expr) bool {
	if this, ok := e.(*dag.This); ok {
		if len(this.Path) == 0 {
			return true
		}
	}
	return false
}

func (c *canonDAG) maybewrite(s string, do bool) {
	if do {
		c.write(s)
	}
}

func (c *canonDAG) next() {
	if c.first {
		c.first = false
	} else {
		c.write("\n")
	}
	c.needRet = false
	c.writeTab()
	if c.head {
		c.head = false
	} else {
		c.write("| ")
	}
}

func (c *canonDAG) op(p dag.Op) {
	switch p := p.(type) {
	case *dag.Sequential:
		if p == nil {
			return
		}
		for _, d := range p.Consts {
			c.write("const %s = ", d.Name)
			c.expr(d.Expr, "")
			c.ret()
		}
		for _, f := range p.Funcs {
			c.write("func %s(", f.Name)
			for i := range f.Params {
				if i != 0 {
					c.write(", ")
				}
				c.write(f.Params[i])
			}
			c.open("): (")
			c.ret()
			c.expr(f.Expr, f.Name)
			c.close()
			c.ret()
			c.flush()
			c.write(")\n")
		}
		for _, p := range p.Ops {
			c.op(p)
		}
	case *dag.Parallel:
		c.next()
		c.open("fork (")
		for _, p := range p.Ops {
			c.ret()
			c.write("=>")
			c.open()
			c.head = true
			c.op(p)
			c.close()
		}
		c.close()
		c.ret()
		c.flush()
		c.write(")")
	case *dag.Switch:
		c.next()
		c.open("switch ")
		if p.Expr != nil {
			c.expr(p.Expr, "")
			c.write(" ")
		}
		c.open("(")
		for _, k := range p.Cases {
			c.ret()
			if k.Expr != nil {
				c.write("case ")
				c.expr(k.Expr, "")
			} else {
				c.write("default")
			}
			c.write(" =>")
			c.open()
			c.head = true
			c.op(k.Op)
			c.close()
		}
		c.close()
		c.ret()
		c.flush()
		c.write(")")
	case *dag.Merge:
		c.next()
		c.write("merge ")
		c.expr(p.Expr, "")
		c.write(":" + p.Order.String())
	case *dag.Summarize:
		c.next()
		c.open("summarize")
		if p.PartialsIn {
			c.write(" partials-in")
		}
		if p.PartialsOut {
			c.write(" partials-out")
		}
		if p.InputSortDir != 0 {
			c.write(" sort-dir %d", p.InputSortDir)
		}
		c.ret()
		c.open()
		c.assignments(p.Aggs)
		if len(p.Keys) != 0 {
			c.write(" by ")
			c.assignments(p.Keys)
		}
		if p.Limit != 0 {
			c.write(" -with limit %d", p.Limit)
		}
		c.close()
		c.close()
	case *dag.Cut:
		c.next()
		c.write("cut ")
		c.assignments(p.Args)
	case *dag.Drop:
		c.next()
		c.write("drop ")
		c.exprs(p.Args)
	case *dag.Sort:
		c.next()
		c.write("sort")
		if p.Order == order.Desc {
			c.write(" -r")
		}
		if p.NullsFirst {
			c.write(" -nulls first")
		}
		if len(p.Args) > 0 {
			c.space()
			c.exprs(p.Args)
		}
	case *dag.Head:
		c.next()
		c.write("head %d", p.Count)
	case *dag.Tail:
		c.next()
		c.write("tail %d", p.Count)
	case *dag.Uniq:
		c.next()
		c.write("uniq")
		if p.Cflag {
			c.write(" -c")
		}
	case *dag.Pass:
		c.next()
		c.write("pass")
	case *dag.Filter:
		c.next()
		c.open("where ")
		if isDAGTrue(p.Expr) {
			c.write("*")
		} else {
			c.expr(p.Expr, "")
		}
		c.close()
	case *dag.Top:
		c.next()
		c.write("top limit=%d flush=%t ", p.Limit, p.Flush)
		c.exprs(p.Args)
	case *dag.Put:
		c.next()
		c.write("put ")
		c.assignments(p.Args)
	case *dag.Rename:
		c.next()
		c.write("rename ")
		c.assignments(p.Args)
	case *dag.Fuse:
		c.next()
		c.write("fuse")
	case *dag.Join:
		c.next()
		c.open("join on ")
		c.expr(p.LeftKey, "")
		c.write("=")
		c.expr(p.RightKey, "")
		if len(p.Args) != 0 {
			c.write(" ")
			c.assignments(p.Args)
		}
		c.close()
	case *dag.From:
		// XXX cleanup for single trunk
		c.next()
		c.open("from (")
		for _, trunk := range p.Trunks {
			c.ret()
			if trunk.Pushdown != nil {
				c.head = true
				c.open("(pushdown")
				c.op(trunk.Pushdown)
				c.write(")")
				c.close()
				c.ret()
			}
			c.write("%s", source(trunk.Source))
			if trunk.Seq != nil && len(trunk.Seq.Ops) != 0 {
				c.open()
				c.head = true
				c.write(" =>")
				c.op(trunk.Seq)
				c.close()
			}
		}
		c.ret()
		c.close()
		c.write(")")
	case *dag.Over:
		c.next()
		c.write("over ")
		c.exprs(p.Exprs)
	case *dag.Yield:
		c.next()
		c.write("yield ")
		c.exprs(p.Exprs)
	default:
		c.open("unknown proc: %T", p)
		c.close()
	}
}

func source(src dag.Source) string {
	switch p := src.(type) {
	case *dag.File:
		s := fmt.Sprintf("file %s", p.Path)
		if p.Format != "" {
			s += fmt.Sprintf(" format %s", p.Format)
		}
		if !p.Layout.IsNil() {
			s += fmt.Sprintf(" order %s", p.Layout)
		}
		return s
	case *dag.HTTP:
		return fmt.Sprintf("get %s", p.URL)
	case *dag.Pool:
		return fmt.Sprintf("pool %s", p.ID)
	case *dag.PoolMeta:
		return fmt.Sprintf("pool %s:%s", p.ID, p.Meta)
	case *dag.CommitMeta:
		return fmt.Sprintf("pool %s@%s:%s", p.Pool, p.Commit, p.Meta)
	case *dag.LakeMeta:
		return fmt.Sprintf(":%s", p.Meta)
		//XXX from, to, order
	case *dag.Pass:
		return "pass"
	case *kernel.Reader:
		return "(internal reader)"
	default:
		return fmt.Sprintf("unknown source %T", p)
	}
}

func isDAGTrue(e dag.Expr) bool {
	if p, ok := e.(*astzed.Primitive); ok {
		return p.Type == "bool" && p.Text == "true"
	}
	return false
}
