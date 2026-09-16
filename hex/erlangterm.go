package hex

// Minimal Erlang external term format (version 131) encoder for the shapes
// hex_core's Accept: application/vnd.hex+erlang requests expect. Only the
// subset needed by the hex client is implemented: maps, lists, binaries,
// small ints, atoms, nil.

const erlangVersion = 131

func appendU16(b []byte, v uint16) []byte {
	return append(b, byte(v>>8), byte(v))
}

func appendU32(b []byte, v uint32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// encodeTerm appends the external-format encoding of v.
func encodeTerm(b []byte, v any) []byte {
	switch t := v.(type) {
	case string:
		// binary ext: 109 len:32 flags:8 data
		b = append(b, 109)
		b = appendU32(b, uint32(len(t)))
		b = append(b, 0)
		return append(b, t...)
	case int:
		// small/big int — use INTEGER_EXT (98) for the common range
		return append(b, 98, byte(t>>24), byte(t>>16), byte(t>>8), byte(t))
	case bool:
		if t {
			return appendAtom(b, "true")
		}
		return appendAtom(b, "false")
	case nil:
		return append(b, 106) // NIL (empty list)
	case []any:
		if len(t) == 0 {
			return append(b, 106)
		}
		b = append(b, 108) // LIST_EXT
		b = appendU32(b, uint32(len(t)))
		for _, x := range t {
			b = encodeTerm(b, x)
		}
		return append(b, 106) // tail
	case map[string]any:
		b = append(b, 116) // MAP_EXT
		b = appendU32(b, uint32(len(t)))
		for k, val := range t {
			b = encodeTerm(b, k)
			b = encodeTerm(b, val)
		}
		return b
	default:
		// Fallback: encode as an empty map rather than corrupting the stream.
		b = append(b, 116)
		return appendU32(b, 0)
	}
}

func appendAtom(b []byte, name string) []byte {
	// ATOM_EXT (100) is deprecated but universally supported; use ATOM_UTF8_EXT
	// (118) for correctness.
	b = append(b, 118)
	b = appendU16(b, uint16(len(name)))
	return append(b, name...)
}

// termToBinary wraps encoded terms with the version tag.
func termToBinary(v any) []byte {
	return encodeTerm([]byte{erlangVersion}, v)
}

// erlangUsersMe encodes the /users/me payload hex_core decodes:
// #{username => <<"lc">>, organizations => []}.
func erlangUsersMe() []byte {
	return termToBinary(map[string]any{
		"username":      "lc",
		"email":         "lc@easylab.invalid",
		"organizations": []any{},
	})
}
