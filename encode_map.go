// Copyright (c) Faye Amacker. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package cbor

import (
	"bytes"
	"reflect"
	"slices"
	"sync"
)

type mapKeyValueEncodeFunc struct {
	kf, ef       encodeFunc
	kpool, vpool sync.Pool
}

// suspendKeySharing runs fn with shareable-value tracking suspended, so map
// keys never consume shareable indices.  Index numbering then depends only
// on map values, which keeps deterministic output independent of how keys
// are reordered.  It must be called with the private per-encode-run em.
func suspendKeySharing(em *encMode, fn func() error) error {
	if em.encSharing == nil {
		return fn()
	}
	saved := em.encSharing
	em.encSharing = nil
	err := fn()
	em.encSharing = saved
	return err
}

func (me *mapKeyValueEncodeFunc) encodeKeyValues(dst []byte, em *encMode, v reflect.Value, kvs []keyValue) ([]byte, error) {
	iterk := me.kpool.Get().(*reflect.Value)
	defer func() {
		iterk.SetZero()
		me.kpool.Put(iterk)
	}()
	iterv := me.vpool.Get().(*reflect.Value)
	defer func() {
		iterv.SetZero()
		me.vpool.Put(iterv)
	}()

	var err error

	if kvs == nil {
		for i, iter := 0, v.MapRange(); iter.Next(); i++ {
			iterk.SetIterKey(iter)
			iterv.SetIterValue(iter)

			if err = suspendKeySharing(em, func() error {
				dst, err = me.kf(dst, em, *iterk)
				return err
			}); err != nil {
				return dst, err
			}
			if dst, err = me.ef(dst, em, *iterv); err != nil {
				return dst, err
			}
		}
		return dst, nil
	}

	initial := len(dst)
	for i, iter := 0, v.MapRange(); iter.Next(); i++ {
		iterk.SetIterKey(iter)
		iterv.SetIterValue(iter)

		offset := len(dst)
		if err = suspendKeySharing(em, func() error {
			dst, err = me.kf(dst, em, *iterk)
			return err
		}); err != nil {
			return dst, err
		}
		valueOffset := len(dst)
		if dst, err = me.ef(dst, em, *iterv); err != nil {
			return dst, err
		}
		kvs[i] = keyValue{
			offset:      offset - initial,
			valueOffset: valueOffset - initial,
			nextOffset:  len(dst) - initial,
		}
	}

	return dst, nil
}

// encodedMapKey pairs an encoded map key with its yet-to-encode value.
type encodedMapKey struct {
	start, end int // key byte range inside the arena
	value      reflect.Value
}

// encodeSortedMapKeyValuesShared encodes map entries in deterministic sorted
// key order while value sharing is enabled.  Keys are encoded first with
// sharing suspended, sorted using the same key order as the rest of the
// encoder, and only then are values encoded in final output order, so
// shareable index assignment is stable.  The map head is already emitted.
func encodeSortedMapKeyValuesShared(dst []byte, em *encMode, v reflect.Value) ([]byte, error) {
	keys := make([]encodedMapKey, 0, v.Len())
	arena := make([]byte, 0, v.Len()*8)

	var err error
	for iter := v.MapRange(); iter.Next(); {
		start := len(arena)
		if e := suspendKeySharing(em, func() error {
			var e error
			arena, e = encode(arena, em, iter.Key())
			return e
		}); e != nil {
			return dst, e
		}
		keys = append(keys, encodedMapKey{
			start: start,
			end:   len(arena),
			value: iter.Value(),
		})
	}

	slices.SortFunc(keys, func(a, b encodedMapKey) int {
		ka, kb := arena[a.start:a.end], arena[b.start:b.end]
		if em.sort == SortBytewiseLexical {
			return bytes.Compare(ka, kb)
		}
		if keyLengthDifference := len(ka) - len(kb); keyLengthDifference != 0 {
			return keyLengthDifference
		}
		return bytes.Compare(ka, kb)
	})

	for _, k := range keys {
		dst = append(dst, arena[k.start:k.end]...)
		if dst, err = encode(dst, em, k.value); err != nil {
			return dst, err
		}
	}
	return dst, nil
}

func getEncodeMapFunc(t reflect.Type, newEncodeFuncs map[reflect.Type]*inProgressEncodeFuncs) encodeFunc {
	kf := getEncodeFuncWithNewEncodeFuncs(t.Key(), newEncodeFuncs)
	ef := getEncodeFuncWithNewEncodeFuncs(t.Elem(), newEncodeFuncs)
	if kf == nil || ef == nil {
		return nil
	}
	mkv := &mapKeyValueEncodeFunc{
		kf: kf,
		ef: ef,
		kpool: sync.Pool{
			New: func() any {
				rk := reflect.New(t.Key()).Elem()
				return &rk
			},
		},
		vpool: sync.Pool{
			New: func() any {
				rv := reflect.New(t.Elem()).Elem()
				return &rv
			},
		},
	}
	return mapEncodeFunc{
		e: mkv.encodeKeyValues,
	}.encode
}
