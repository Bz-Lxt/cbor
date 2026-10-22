// Copyright (c) Faye Amacker. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package cbor

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"testing"
)

func sharingTestModes(t *testing.T) (EncMode, DecMode) {
	t.Helper()
	em, err := EncOptions{ValueSharing: ValueSharingAllowed}.EncMode()
	if err != nil {
		t.Fatalf("EncMode() error: %v", err)
	}
	dm, err := DecOptions{ValueSharing: ValueSharingAllowed}.DecMode()
	if err != nil {
		t.Fatalf("DecMode() error: %v", err)
	}
	return em, dm
}

func canonicalSharingModes(t *testing.T) (EncMode, DecMode) {
	t.Helper()
	opts := CoreDetEncOptions()
	opts.ValueSharing = ValueSharingAllowed
	em, err := opts.EncMode()
	if err != nil {
		t.Fatalf("EncMode() error: %v", err)
	}
	dm, err := DecOptions{ValueSharing: ValueSharingAllowed}.DecMode()
	if err != nil {
		t.Fatalf("DecMode() error: %v", err)
	}
	return em, dm
}

type sharingNode struct {
	N    int
	Next *sharingNode
}

func TestSharingDuplicatePointerRoundtrip(t *testing.T) {
	em, dm := sharingTestModes(t)

	type node struct {
		X int
	}
	p := &node{X: 7}
	in := []any{p, p}

	b, err := em.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	// 28([...]) and 29(...) must both be present.
	if !bytes.Contains(b, []byte{0xd8, 0x1c}) {
		t.Errorf("encoded data %x is missing shareable tag 28", b)
	}
	if !bytes.Contains(b, []byte{0xd8, 0x1d}) {
		t.Errorf("encoded data %x is missing sharedref tag 29", b)
	}

	// Typed decode preserves identity.
	var out []*node
	if err := dm.Unmarshal(b, &out); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}
	if len(out) != 2 || out[0] == nil || out[0] != out[1] {
		t.Fatalf("expected both elements to share identity, got %+v", out)
	}
	if out[0].X != 7 {
		t.Fatalf("expected X=7, got %d", out[0].X)
	}
}

func TestSharingDuplicateMapAndSliceRoundtrip(t *testing.T) {
	em, dm := canonicalSharingModes(t)

	m := map[string]any{"a": 1}
	s := []any{1, 2}
	in := map[string]any{"first": m, "second": m, "s1": s, "s2": s}

	b, err := em.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	var out map[string]any
	if err := dm.Unmarshal(b, &out); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}
	first, ok1 := out["first"]
	second, ok2 := out["second"]
	s1, ok3 := out["s1"].([]any)
	s2, ok4 := out["s2"].([]any)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		t.Fatalf("unexpected decoded shape: %#v", out)
	}
	if reflect.ValueOf(first).Pointer() != reflect.ValueOf(second).Pointer() {
		t.Errorf("map values don't share identity: %#v vs %#v", first, second)
	}
	if reflect.ValueOf(s1).Pointer() != reflect.ValueOf(s2).Pointer() {
		t.Errorf("slice values don't share identity: %#v vs %#v", s1, s2)
	}
}

func TestSharingCyclicPointer(t *testing.T) {
	em, dm := canonicalSharingModes(t)

	n := &sharingNode{N: 1}
	n.Next = n

	b, err := em.Marshal(n)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	// 28({"N":1,"Next":29(0)})
	if !bytes.Contains(b, []byte{0xd8, 0x1d, 0x00}) {
		t.Fatalf("encoded cycle %x is missing 29(0)", b)
	}

	var out *sharingNode
	if err := dm.Unmarshal(b, &out); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}
	if out.Next != out {
		t.Fatalf("expected self reference, got Next=%p out=%p", out.Next, out)
	}
	if out.N != 1 {
		t.Fatalf("expected N=1, got %d", out.N)
	}
}

func TestSharingCyclicMapIntoAny(t *testing.T) {
	em, dm := canonicalSharingModes(t)

	m := map[string]any{}
	m["self"] = m
	m["n"] = 3

	b, err := em.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	var out any
	if err := dm.Unmarshal(b, &out); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}
	om, ok := out.(map[any]any)
	if !ok {
		t.Fatalf("expected map[any]any, got %T", out)
	}
	back, ok := om["self"].(map[any]any)
	if !ok {
		t.Fatalf("expected self to be map, got %T (%#v)", om["self"], om["self"])
	}
	if reflect.ValueOf(om).Pointer() != reflect.ValueOf(back).Pointer() {
		t.Fatalf("self map is not the same map")
	}
}

func TestSharingCyclicSlice(t *testing.T) {
	em, dm := canonicalSharingModes(t)

	s := make([]any, 2)
	s[0] = 1
	s[1] = s

	b, err := em.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	var out []any
	if err := dm.Unmarshal(b, &out); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}
	inner, ok := out[1].([]any)
	if !ok {
		t.Fatalf("expected inner []any, got %T", out[1])
	}
	if reflect.ValueOf(out).Pointer() != reflect.ValueOf(inner).Pointer() {
		t.Fatalf("slice is not self-referential")
	}
}

func TestSharingDeterministicOutput(t *testing.T) {
	em, _ := canonicalSharingModes(t)

	m1 := map[string]any{"k1": 1, "k2": 2}
	m2 := map[string]any{"x": m1, "y": m1, "z": map[string]any{"deep": m1}}

	b1, err := em.Marshal(m2)
	if err != nil {
		t.Fatalf("Marshal() first error: %v", err)
	}
	b2, err := em.Marshal(m2)
	if err != nil {
		t.Fatalf("Marshal() second error: %v", err)
	}
	if !bytes.Equal(b1, b2) {
		t.Fatalf("non-deterministic output:\n%x\n%x", b1, b2)
	}
}

func TestSharingNamespaceIsolation(t *testing.T) {
	em, dm := canonicalSharingModes(t)

	innerA := map[string]any{"a": 1}
	innerB := map[string]any{"b": 2}
	ns := Tag{Number: 296, Content: []any{innerA, innerA, innerB}}
	outerA := map[string]any{"o": 1}
	in := []any{ns, outerA, outerA}

	b, err := em.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	// Outer references must restart at index 0 inside their own namespace.
	// Expect outer pair 28(...) ... 29(0).
	if !bytes.Contains(b, []byte{0xd9, 0x01, 0x28}) {
		t.Fatalf("encoded data %x is missing tag 296", b)
	}

	var out []any
	if err := dm.Unmarshal(b, &out); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 elements, got %d", len(out))
	}
	nsOut, ok := out[0].([]any)
	if !ok || len(nsOut) != 3 {
		t.Fatalf("namespace content expected []any of length 3, got %T", out[0])
	}
	nsA1, ok := nsOut[0].(map[any]any)
	if !ok {
		t.Fatalf("expected inner map, got %T", nsOut[0])
	}
	nsA2, ok := nsOut[1].(map[any]any)
	if !ok {
		t.Fatalf("expected inner map ref, got %T", nsOut[1])
	}
	if reflect.ValueOf(nsA1).Pointer() != reflect.ValueOf(nsA2).Pointer() {
		t.Fatalf("inner references lost identity inside namespace")
	}
	o1, ok := out[1].(map[any]any)
	if !ok {
		t.Fatalf("expected outer map, got %T", out[1])
	}
	o2, ok := out[2].(map[any]any)
	if !ok {
		t.Fatalf("expected outer map ref, got %T", out[2])
	}
	if reflect.ValueOf(o1).Pointer() != reflect.ValueOf(o2).Pointer() {
		t.Fatalf("outer references lost identity across namespace")
	}
}

func TestSharingConsecutiveItemsIsolated(t *testing.T) {
	em, dm := canonicalSharingModes(t)

	// Stream encode two independent data items.
	var buf bytes.Buffer
	enc := em.NewEncoder(&buf)
	p1 := map[string]any{"item": 1}
	p2 := map[string]any{"item": 2}
	if err := enc.Encode([]any{p1, p1}); err != nil {
		t.Fatalf("Encode() first error: %v", err)
	}
	if err := enc.Encode([]any{p2, p2}); err != nil {
		t.Fatalf("Encode() second error: %v", err)
	}

	dec := dm.NewDecoder(bytes.NewReader(buf.Bytes()))
	var first []any
	if err := dec.Decode(&first); err != nil {
		t.Fatalf("Decode() first error: %v", err)
	}
	var second []any
	if err := dec.Decode(&second); err != nil {
		t.Fatalf("Decode() second error: %v", err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		t.Fatalf("expected io.EOF, got %v", err)
	}
	if reflect.ValueOf(first[0]).Pointer() != reflect.ValueOf(first[1]).Pointer() {
		t.Fatalf("first item references lost identity")
	}
	if reflect.ValueOf(second[0]).Pointer() != reflect.ValueOf(second[1]).Pointer() {
		t.Fatalf("second item references lost identity")
	}
	if reflect.ValueOf(first[0]).Pointer() == reflect.ValueOf(second[0]).Pointer() {
		t.Fatalf("identity leaked between consecutive data items")
	}
}

func TestSharingConcurrentEncodesDoNotLeakState(t *testing.T) {
	em, dm := canonicalSharingModes(t)

	const goroutines = 32
	const iterations = 50
	errCh := make(chan error, goroutines)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				n := &sharingNode{N: seed*iterations + i}
				n.Next = n
				b, err := em.Marshal(n)
				if err != nil {
					errCh <- err
					return
				}
				var out *sharingNode
				if err := dm.Unmarshal(b, &out); err != nil {
					errCh <- err
					return
				}
				if out.Next != out || out.N != n.N {
					errCh <- fmt.Errorf("goroutine %d iteration %d got %+v", seed, i, out)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

func TestSharingIndefiniteLengthContainers(t *testing.T) {
	dm, err := DecOptions{ValueSharing: ValueSharingAllowed}.DecMode()
	if err != nil {
		t.Fatalf("DecMode() error: %v", err)
	}

	// 28([ 28({"k":5}), 29(1) ]) with indefinite-length array and map.
	// Index 0 is the outer array, index 1 the map.
	data := []byte{
		0xd8, 0x1c, // 28(
		0x9f,       // indefinite-length array
		0xd8, 0x1c, //   28(
		0xbf, 0x61, 0x6b, 0x05, 0xff, //     {"k":5} indefinite-length map
		0xd8, 0x1d, 0x01, //   29(1)
		0xff, // end array
	}
	var out []any
	if err := dm.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}
	m0, ok := out[0].(map[any]any)
	if !ok {
		t.Fatalf("expected map, got %T", out[0])
	}
	m1, ok := out[1].(map[any]any)
	if !ok {
		t.Fatalf("expected map ref, got %T", out[1])
	}
	if reflect.ValueOf(m0).Pointer() != reflect.ValueOf(m1).Pointer() {
		t.Fatalf("identity lost with indefinite-length containers")
	}
}

func TestSharingIllegalReference(t *testing.T) {
	_, dm := sharingTestModes(t)

	tests := []struct {
		name string
		data []byte
	}{
		{
			name: "index out of range",
			data: []byte{0xd8, 0x1d, 0x18, 0x63}, // 29(99)
		},
		{
			name: "non-unsigned index",
			data: []byte{0xd8, 0x1d, 0x20}, // 29(-1)
		},
		{
			name: "unresolved forward reference",
			data: []byte{0x81, 0xd8, 0x1d, 0x00}, // [29(0)]
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v any
			err := dm.Unmarshal(tt.data, &v)
			if err == nil {
				t.Fatalf("expected error for %x, got %#v", tt.data, v)
			}
			var sre *SharedReferenceError
			if !errors.As(err, &sre) {
				t.Fatalf("expected *SharedReferenceError, got %T: %v", err, err)
			}
		})
	}
}

func TestSharingForwardReference(t *testing.T) {
	_, dm := sharingTestModes(t)

	// [29(0), 28({"a":1})]
	data := []byte{
		0x82,
		0xd8, 0x1d, 0x00,
		0xd8, 0x1c, 0xa1, 0x61, 0x61, 0x01,
	}
	var out []any
	if err := dm.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}
	m0, ok := out[0].(map[any]any)
	if !ok {
		t.Fatalf("expected first element map, got %T", out[0])
	}
	m1, ok := out[1].(map[any]any)
	if !ok {
		t.Fatalf("expected second element map, got %T", out[1])
	}
	if reflect.ValueOf(m0).Pointer() != reflect.ValueOf(m1).Pointer() {
		t.Fatalf("forward reference didn't resolve to identical map")
	}
}

func TestSharingDisabledExpandsDuplicates(t *testing.T) {
	em, err := EncOptions{}.EncMode()
	if err != nil {
		t.Fatalf("EncMode() error: %v", err)
	}

	type node struct {
		X int
	}
	p := &node{X: 5}
	b, err := em.Marshal([]any{p, p})
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	if bytes.Contains(b, []byte{0xd8, 0x1c}) || bytes.Contains(b, []byte{0xd8, 0x1d}) {
		t.Fatalf("disabled mode emitted sharing tags: %x", b)
	}
	if n := bytes.Count(b, []byte{0x61, 0x58, 0x05}); n != 2 {
		t.Fatalf("expected duplicate value encoded twice in %x, found %d copies", b, n)
	}
}

func TestSharingOptionsIncompatible(t *testing.T) {
	if _, err := (EncOptions{ValueSharing: ValueSharingAllowed, TagsMd: TagsForbidden}).EncMode(); err == nil {
		t.Fatal("expected error when enabling value sharing with TagsForbidden (encode)")
	}
	if _, err := (DecOptions{ValueSharing: ValueSharingAllowed, TagsMd: TagsForbidden}).DecMode(); err == nil {
		t.Fatal("expected error when enabling value sharing with TagsForbidden (decode)")
	}
	if _, err := (EncOptions{ValueSharing: ValueSharingAllowed, Sort: SortFastShuffle}).EncMode(); err == nil {
		t.Fatal("expected error when enabling value sharing with SortFastShuffle")
	}
	if _, err := (EncOptions{ValueSharing: ValueSharingMode(99)}).EncMode(); err == nil {
		t.Fatal("expected error for invalid ValueSharingMode (encode)")
	}
	if _, err := (DecOptions{ValueSharing: ValueSharingMode(99)}).DecMode(); err == nil {
		t.Fatal("expected error for invalid ValueSharingMode (decode)")
	}
}

func TestSharingOptionsRoundtrip(t *testing.T) {
	em, _ := EncOptions{ValueSharing: ValueSharingAllowed}.EncMode()
	if got := em.EncOptions().ValueSharing; got != ValueSharingAllowed {
		t.Fatalf("EncOptions() roundtrip = %v, want %v", got, ValueSharingAllowed)
	}
	dm, _ := DecOptions{ValueSharing: ValueSharingAllowed}.DecMode()
	if got := dm.DecOptions().ValueSharing; got != ValueSharingAllowed {
		t.Fatalf("DecOptions() roundtrip = %v, want %v", got, ValueSharingAllowed)
	}
}

// sharingBoundaryType emits a self-contained CBOR data item that itself
// contains a shareable tag, so the outer index table has to skip over it.
type sharingBoundaryType struct{ V int }

func (s *sharingBoundaryType) MarshalCBOR() ([]byte, error) {
	// [28(42)] — a shareable tag nested inside the opaque boundary.
	return []byte{0x81, 0xd8, 0x1c, 0x18, 42}, nil
}

func (s *sharingBoundaryType) UnmarshalCBOR(b []byte) error {
	s.V = 42
	return nil
}

type sharingBoundaryOuter struct {
	A *sharingBoundaryType
	B *sharingNode
	C *sharingNode
}

func TestSharingMarshalerBoundary(t *testing.T) {
	em, dm := canonicalSharingModes(t)

	in := &sharingBoundaryOuter{
		A: &sharingBoundaryType{},
		B: &sharingNode{N: 9},
	}
	in.C = in.B

	b, err := em.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	var out sharingBoundaryOuter
	if err := dm.Unmarshal(b, &out); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}
	if out.A == nil || out.A.V != 42 {
		t.Fatalf("Marshaler boundary value not decoded: %+v", out.A)
	}
	if out.B != out.C || out.B.N != 9 {
		t.Fatalf("outer references after boundary lost identity: %+v", out)
	}
}

func TestSharingSkippedUnknownFieldKeepsIndicesAligned(t *testing.T) {
	em, dm := canonicalSharingModes(t)

	// Core deterministic ordering puts the short unknown key "a" first,
	// so its shareable marker shifts the indices of the kept references.
	dropped := &sharingNode{N: 1}
	kept := &sharingNode{N: 2}
	in := map[string]any{
		"a":  dropped,
		"bb": kept,
		"cc": kept,
	}
	b, err := em.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	type keepStruct struct {
		First  *sharingNode `cbor:"bb"`
		Second *sharingNode `cbor:"cc"`
	}
	var out keepStruct
	if err := dm.Unmarshal(b, &out); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}
	if out.First != out.Second {
		t.Fatalf("references misaligned after skipped field: %+v", out)
	}
	if out.First == nil || out.First.N != 2 {
		t.Fatalf("unexpected decoded value: %+v", out.First)
	}
}

func TestCountShareableTagsIgnoresNestedNamespace(t *testing.T) {
	// [296([28(42)])]: the shareable marker is inside a nested namespace
	// and must not count towards the enclosing table.
	data := []byte{
		0x81,
		0xd9, 0x01, 0x28,
		0x81, 0xd8, 0x1c, 0x18, 0x2a,
	}
	if n := countShareableTags(data); n != 0 {
		t.Fatalf("countShareableTags() = %d, want 0 for nested namespace content", n)
	}
	// Without a namespace wrapper, the marker counts.
	data = []byte{0x81, 0xd8, 0x1c, 0x18, 0x2a}
	if n := countShareableTags(data); n != 1 {
		t.Fatalf("countShareableTags() = %d, want 1", n)
	}
}

func TestSharingReferenceIntoMarshalerBoundaryFails(t *testing.T) {
	_, dm := sharingTestModes(t)

	// Array [28([28(42)]), 29(1)]: index 1 is a shareable inside the
	// unmarshaler region of the first element, referencing it must fail.
	data := []byte{
		0x82,
		0xd8, 0x1c, 0x81, 0xd8, 0x1c, 0x18, 0x2a,
		0xd8, 0x1d, 0x01,
	}
	var out []*sharingBoundaryType
	err := dm.Unmarshal(data, &out)
	if err == nil {
		t.Fatalf("expected error referencing opaque boundary, got %#v", out)
	}
	var sre *SharedReferenceError
	if !errors.As(err, &sre) {
		t.Fatalf("expected *SharedReferenceError, got %T: %v", err, err)
	}
}
