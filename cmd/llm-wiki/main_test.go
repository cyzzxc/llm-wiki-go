package main

import (
	"reflect"
	"testing"
)

// B1 回归：布尔 flag 不吞下一个位置参数。
func TestParseFlagsBoolFlagsDoNotSwallowPositional(t *testing.T) {
	pos, flags := parseFlags([]string{"search", "--semantic", "注意力机制"})
	if !reflect.DeepEqual(pos, []string{"search", "注意力机制"}) {
		t.Fatalf("positional = %v", pos)
	}
	if flags["semantic"] != "true" {
		t.Fatalf("semantic = %q", flags["semantic"])
	}

	pos, flags = parseFlags([]string{"--hybrid", "门控", "路由"})
	if !reflect.DeepEqual(pos, []string{"门控", "路由"}) {
		t.Fatalf("positional = %v", pos)
	}
	if flags["hybrid"] != "true" {
		t.Fatalf("hybrid = %q", flags["hybrid"])
	}
}

// 值 flag 仍然吃下一个 token；= 形式不受影响。
func TestParseFlagsValueFlagsStillConsume(t *testing.T) {
	pos, flags := parseFlags([]string{"search", "--top-k", "5", "--format=json", "查询词"})
	if !reflect.DeepEqual(pos, []string{"search", "查询词"}) {
		t.Fatalf("positional = %v", pos)
	}
	if flags["top-k"] != "5" || flags["format"] != "json" {
		t.Fatalf("flags = %v", flags)
	}
}
