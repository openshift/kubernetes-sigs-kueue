package yaml

import (
	"bytes"
	"context"
	"encoding"
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"time"

	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/internal/errors"
	"github.com/goccy/go-yaml/parser"
	"github.com/goccy/go-yaml/token"
)

// Decoder reads and decodes YAML values from an input stream.
type Decoder struct {
	reader               io.Reader
	referenceReaders     []io.Reader
	anchorNodeMap        map[string]ast.Node
	aliasValueMap        map[*ast.AliasNode]any
	anchorValueMap       map[string]reflect.Value
	customUnmarshalerMap map[reflect.Type]func(interface{}, []byte) error
	toCommentMap         CommentMap
	opts                 []DecodeOption
	referenceFiles       []string
	referenceDirs        []string
	isRecursiveDir       bool
	isResolvedReference  bool
	validator            StructValidator
	disallowUnknownField bool
	disallowDuplicateKey bool
	useOrderedMap        bool
	useJSONUnmarshaler   bool
	parsedFile           *ast.File
	streamIndex          int
}

// NewDecoder returns a new decoder that reads from r.
func NewDecoder(r io.Reader, opts ...DecodeOption) *Decoder {
	return &Decoder{
		reader:               r,
		anchorNodeMap:        map[string]ast.Node{},
		aliasValueMap:        make(map[*ast.AliasNode]any),
		anchorValueMap:       map[string]reflect.Value{},
		customUnmarshalerMap: map[reflect.Type]func(interface{}, []byte) error{},
		opts:                 opts,
		referenceReaders:     []io.Reader{},
		referenceFiles:       []string{},
		referenceDirs:        []string{},
		isRecursiveDir:       false,
		isResolvedReference:  false,
		disallowUnknownField: false,
		disallowDuplicateKey: false,
		useOrderedMap:        false,
	}
}

func (d *Decoder) castToFloat(v interface{}) interface{} {
	switch vv := v.(type) {
	case int:
		return float64(vv)
	case int8:
		return float64(vv)
	case int16:
		return float64(vv)
	case int32:
		return float64(vv)
	case int64:
		return float64(vv)
	case uint:
		return float64(vv)
	case uint8:
		return float64(vv)
	case uint16:
		return float64(vv)
	case uint32:
		return float64(vv)
	case uint64:
		return float64(vv)
	case float32:
		return float64(vv)
	case float64:
		return vv
	case string:
		// if error occurred, return zero value
		f, _ := strconv.ParseFloat(vv, 64)
		return f
	}
	return 0
}

func (d *Decoder) mergeValueNode(value ast.Node) ast.Node {
	if value.Type() == ast.AliasType {
		aliasNode, _ := value.(*ast.AliasNode)
		aliasName := aliasNode.Value.GetToken().Value
		return d.anchorNodeMap[aliasName]
	}
	return value
}

func (d *Decoder) mapKeyNodeToString(node ast.MapKeyNode) (string, error) {
	key, err := d.nodeToValue(node)
	if err != nil {
		return "", err
	}
	if key == nil {
		return "null", nil
	}
	if k, ok := key.(string); ok {
		return k, nil
	}
	return fmt.Sprint(key), nil
}

func (d *Decoder) setToMapValue(node ast.Node, m map[string]interface{}) error {
	d.setPathToCommentMap(node)
	switch n := node.(type) {
	case *ast.MappingValueNode:
		if n.Key.Type() == ast.MergeKeyType {
			if err := d.setToMapValue(d.mergeValueNode(n.Value), m); err != nil {
				return err
			}
		} else {
			key, err := d.mapKeyNodeToString(n.Key)
			if err != nil {
				return err
			}
			v, err := d.nodeToValue(n.Value)
			if err != nil {
				return err
			}
			m[key] = v
		}
	case *ast.MappingNode:
		for _, value := range n.Values {
			if err := d.setToMapValue(value, m); err != nil {
				return err
			}
		}
	case *ast.AnchorNode:
		anchorName := n.Name.GetToken().Value
		d.anchorNodeMap[anchorName] = n.Value
	}
	return nil
}

func (d *Decoder) setToOrderedMapValue(node ast.Node, m *MapSlice) error {
	switch n := node.(type) {
	case *ast.MappingValueNode:
		if n.Key.Type() == ast.MergeKeyType {
			if err := d.setToOrderedMapValue(d.mergeValueNode(n.Value), m); err != nil {
				return err
			}
		} else {
			key, err := d.mapKeyNodeToString(n.Key)
			if err != nil {
				return err
			}
			value, err := d.nodeToValue(n.Value)
			if err != nil {
				return err
			}
			*m = append(*m, MapItem{Key: key, Value: value})
		}
	case *ast.MappingNode:
		for _, value := range n.Values {
			if err := d.setToOrderedMapValue(value, m); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Decoder) setPathToCommentMap(node ast.Node) {
	if d.toCommentMap == nil {
		return
	}
	d.addHeadOrLineCommentToMap(node)
	d.addFootCommentToMap(node)
}

func (d *Decoder) addHeadOrLineCommentToMap(node ast.Node) {
	sequence, ok := node.(*ast.SequenceNode)
	if ok {
		d.addSequenceNodeCommentToMap(sequence)
		return
	}
	commentGroup := node.GetComment()
	if commentGroup == nil {
		return
	}
	texts := []string{}
	targetLine := node.GetToken().Position.Line
	minCommentLine := math.MaxInt
	for _, comment := range commentGroup.Comments {
		if minCommentLine > comment.Token.Position.Line {
			minCommentLine = comment.Token.Position.Line
		}
		texts = append(texts, comment.Token.Value)
	}
	if len(texts) == 0 {
		return
	}
	commentPath := node.GetPath()
	if minCommentLine < targetLine {
		d.addCommentToMap(commentPath, HeadComment(texts...))
	} else {
		d.addCommentToMap(commentPath, LineComment(texts[0]))
	}
}

func (d *Decoder) addSequenceNodeCommentToMap(node *ast.SequenceNode) {
	if len(node.ValueHeadComments) != 0 {
		for idx, headComment := range node.ValueHeadComments {
			if headComment == nil {
				continue
			}
			texts := make([]string, 0, len(headComment.Comments))
			for _, comment := range headComment.Comments {
				texts = append(texts, comment.Token.Value)
			}
			if len(texts) != 0 {
				d.addCommentToMap(node.Values[idx].GetPath(), HeadComment(texts...))
			}
		}
	}
	firstElemHeadComment := node.GetComment()
	if firstElemHeadComment != nil {
		texts := make([]string, 0, len(firstElemHeadComment.Comments))
		for _, comment := range firstElemHeadComment.Comments {
			texts = append(texts, comment.Token.Value)
		}
		if len(texts) != 0 {
			d.addCommentToMap(node.Values[0].GetPath(), HeadComment(texts...))
		}
	}
}

func (d *Decoder) addFootCommentToMap(node ast.Node) {
	var (
		footComment     *ast.CommentGroupNode
		footCommentPath string = node.GetPath()
	)
	switch n := node.(type) {
	case *ast.SequenceNode:
		if len(n.Values) != 0 {
			footCommentPath = n.Values[len(n.Values)-1].GetPath()
		}
		footComment = n.FootComment
	case *ast.MappingNode:
		footComment = n.FootComment
	case *ast.MappingValueNode:
		footComment = n.FootComment
	}
	if footComment == nil {
		return
	}
	var texts []string
	for _, comment := range footComment.Comments {
		texts = append(texts, comment.Token.Value)
	}
	if len(texts) != 0 {
		d.addCommentToMap(footCommentPath, FootComment(texts...))
	}
}

func (d *Decoder) addCommentToMap(path string, comment *Comment) {
	for _, c := range d.toCommentMap[path] {
		if c.Position == comment.Position {
			// already added same comment
			return
		}
	}
	d.toCommentMap[path] = append(d.toCommentMap[path], comment)
	sort.Slice(d.toCommentMap[path], func(i, j int) bool {
		return d.toCommentMap[path][i].Position < d.toCommentMap[path][j].Position
	})
}

func (d *Decoder) nodeToValue(node ast.Node) (any, error) {
	d.setPathToCommentMap(node)
	switch n := node.(type) {
	case *ast.NullNode:
		return nil, nil
	case *ast.StringNode:
		return n.GetValue(), nil
	case *ast.IntegerNode:
		return n.GetValue(), nil
	case *ast.FloatNode:
		return n.GetValue(), nil
	case *ast.BoolNode:
		return n.GetValue(), nil
	case *ast.InfinityNode:
		return n.GetValue(), nil
	case *ast.NanNode:
		return n.GetValue(), nil
	case *ast.TagNode:
		switch token.ReservedTagKeyword(n.Start.Value) {
		case token.TimestampTag:
			t, _ := d.castToTime(n.Value)
			return t, nil
		case token.IntegerTag:
			v, err := d.nodeToValue(n.Value)
			if err != nil {
				return nil, err
			}
			i, _ := strconv.Atoi(fmt.Sprint(v))
			return i, nil
		case token.FloatTag:
			v, err := d.nodeToValue(n.Value)
			if err != nil {
				return nil, err
			}
			return d.castToFloat(v), nil
		case token.NullTag:
			return nil, nil
		case token.BinaryTag:
			v, err := d.nodeToValue(n.Value)
			if err != nil {
				return nil, err
			}
			b, _ := base64.StdEncoding.DecodeString(v.(string))
			return b, nil
		case token.StringTag:
			return d.nodeToValue(n.Value)
		case token.MappingTag:
			return d.nodeToValue(n.Value)
		}
	case *ast.AnchorNode:
		anchorName := n.Name.GetToken().Value

		// To handle the case where alias is processed recursively, the result of alias can be set to nil in advance.
		d.anchorNodeMap[anchorName] = nil
		anchorValue, err := d.nodeToValue(n.Value)
		if err != nil {
			delete(d.anchorNodeMap, anchorName)
			return nil, err
		}
		d.anchorNodeMap[anchorName] = n.Value
		return anchorValue, nil
	case *ast.AliasNode:
		if v, exists := d.aliasValueMap[n]; exists {
			return v, nil
		}
		// To handle the case where alias is processed recursively, the result of alias can be set to nil in advance.
		d.aliasValueMap[n] = nil

		aliasName := n.Value.GetToken().Value
		node, exists := d.anchorNodeMap[aliasName]
		if !exists {
			return nil, errors.ErrSyntax(fmt.Sprintf("could not find alias %q", aliasName), n.Value.GetToken())
		}
		aliasValue, err := d.nodeToValue(node)
		if err != nil {
			return nil, err
		}

		// once the correct alias value is obtained, overwrite with that value.
		d.aliasValueMap[n] = aliasValue
		return aliasValue, nil
	case *ast.LiteralNode:
		return n.Value.GetValue(), nil
	case *ast.MappingKeyNode:
		return d.nodeToValue(n.Value)
	case *ast.MappingValueNode:
		if n.Key.Type() == ast.MergeKeyType {
			value := d.mergeValueNode(n.Value)
			if d.useOrderedMap {
				m := MapSlice{}
				if err := d.setToOrderedMapValue(value, &m); err != nil {
					return nil, err
				}
				return m, nil
			}
			m := map[string]interface{}{}
			if err := d.setToMapValue(value, m); err != nil {
				return nil, err
			}
			return m, nil
		}
		key, err := d.mapKeyNodeToString(n.Key)
		if err != nil {
			return nil, err
		}
		if d.useOrderedMap {
			v, err := d.nodeToValue(n.Value)
			if err != nil {
				return nil, err
			}
			return MapSlice{{Key: key, Value: v}}, nil
		}
		v, err := d.nodeToValue(n.Value)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{key: v}, nil
	case *ast.MappingNode:
		if d.useOrderedMap {
			m := make(MapSlice, 0, len(n.Values))
			for _, value := range n.Values {
				if err := d.setToOrderedMapValue(value, &m); err != nil {
					return nil, err
				}
			}
			return m, nil
		}
		m := make(map[string]interface{}, len(n.Values))
		for _, value := range n.Values {
			if err := d.setToMapValue(value, m); err != nil {
				return nil, err
			}
		}
		return m, nil
	case *ast.SequenceNode:
		v := make([]interface{}, 0, len(n.Values))
		for _, value := range n.Values {
			vv, err := d.nodeToValue(value)
			if err != nil {
				return nil, err
			}
			v = append(v, vv)
		}
		return v, nil
	}
	return nil, nil
}

func (d *Decoder) resolveAlias(node ast.Node) (ast.Node, error) {
	switch n := node.(type) {
	case *ast.MappingNode:
		for idx, v := range n.Values {
			value, err := d.resolveAlias(v)
			if err != nil {
				return nil, err
			}
			n.Values[idx], _ = value.(*ast.MappingValueNode)
		}
	case *ast.TagNode:
		value, err := d.resolveAlias(n.Value)
		if err != nil {
			return nil, err
		}
		n.Value = value
	case *ast.MappingKeyNode:
		value, err := d.resolveAlias(n.Value)
		if err != nil {
			return nil, err
		}
		n.Value = value
	case *ast.MappingValueNode:
		if n.Key.Type() == ast.MergeKeyType && n.Value.Type() == ast.AliasType {
			value, err := d.resolveAlias(n.Value)
			if err != nil {
				return nil, err
			}
			keyColumn := n.Key.GetToken().Position.Column
			requiredColumn := keyColumn + 2
			value.AddColumn(requiredColumn)
			n.Value = value
		} else {
			key, err := d.resolveAlias(n.Key)
			if err != nil {
				return nil, err
			}
			n.Key, _ = key.(ast.MapKeyNode)
			value, err := d.resolveAlias(n.Value)
			if err != nil {
				return nil, err
			}
			n.Value = value
		}
	case *ast.SequenceNode:
		for idx, v := range n.Values {
			value, err := d.resolveAlias(v)
			if err != nil {
				return nil, err
			}
			n.Values[idx] = value
		}
	case *ast.AliasNode:
		aliasName := n.Value.GetToken().Value
		node := d.anchorNodeMap[aliasName]
		if node == nil {
			return nil, fmt.Errorf("cannot find anchor by alias name %s", aliasName)
		}
		return d.resolveAlias(node)
	}
	return node, nil
}

func (d *Decoder) getMapNode(node ast.Node) (ast.MapNode, error) {
	if _, ok := node.(*ast.NullNode); ok {
		return nil, nil
	}
	if anchor, ok := node.(*ast.AnchorNode); ok {
		mapNode, ok := anchor.Value.(ast.MapNode)
		if ok {
			return mapNode, nil
		}
		return nil, errUnexpectedNodeType(anchor.Value.Type(), ast.MappingType, node.GetToken())
	}
	if alias, ok := node.(*ast.AliasNode); ok {
		aliasName := alias.Value.GetToken().Value
		node := d.anchorNodeMap[aliasName]
		if node == nil {
			return nil, fmt.Errorf("cannot find anchor by alias name %s", aliasName)
		}
		mapNode, ok := node.(ast.MapNode)
		if ok {
			return mapNode, nil
		}
		return nil, errUnexpectedNodeType(node.Type(), ast.MappingType, node.GetToken())
	}
	mapNode, ok := node.(ast.MapNode)
	if !ok {
		return nil, errUnexpectedNodeType(node.Type(), ast.MappingType, node.GetToken())
	}
	return mapNode, nil
}

func (d *Decoder) getArrayNode(node ast.Node) (ast.ArrayNode, error) {
	if _, ok := node.(*ast.NullNode); ok {
		return nil, nil
	}
	if anchor, ok := node.(*ast.AnchorNode); ok {
		arrayNode, ok := anchor.Value.(ast.ArrayNode)
		if ok {
			return arrayNode, nil
		}

		return nil, errUnexpectedNodeType(anchor.Value.Type(), ast.SequenceType, node.GetToken())
	}
	if alias, ok := node.(*ast.AliasNode); ok {
		aliasName := alias.Value.GetToken().Value
		node := d.anchorNodeMap[aliasName]
		if node == nil {
			return nil, fmt.Errorf("cannot find anchor by alias name %s", aliasName)
		}
		arrayNode, ok := node.(ast.ArrayNode)
		if ok {
			return arrayNode, nil
		}
		return nil, errUnexpectedNodeType(node.Type(), ast.SequenceType, node.GetToken())
	}
	arrayNode, ok := node.(ast.ArrayNode)
	if !ok {
		return nil, errUnexpectedNodeType(node.Type(), ast.SequenceType, node.GetToken())
	}
	return arrayNode, nil
}

func (d *Decoder) convertValue(v reflect.Value, typ reflect.Type, src ast.Node) (reflect.Value, error) {
	if typ.Kind() != reflect.String {
		if !v.Type().ConvertibleTo(typ) {

			// Special case for "strings -> floats" aka scientific notation
			// If the destination type is a float and the source type is a string, check if we can
			// use strconv.ParseFloat to convert the string to a float.
			if (typ.Kind() == reflect.Float32 || typ.Kind() == reflect.Float64) &&
				v.Type().Kind() == reflect.String {
				if f, err := strconv.ParseFloat(v.String(), 64); err == nil {
					if typ.Kind() == reflect.Float32 {
						return reflect.ValueOf(float32(f)), nil
					} else if typ.Kind() == reflect.Float64 {
						return reflect.ValueOf(f), nil
					}
					// else, fall through to the error below
				}
			}
			return reflect.Zero(typ), errTypeMismatch(typ, v.Type(), src.GetToken())
		}
		return v.Convert(typ), nil
	}
	// cast value to string
	switch v.Type().Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflect.ValueOf(fmt.Sprint(v.Int())), nil
	case reflect.Float32, reflect.Float64:
		return reflect.ValueOf(fmt.Sprint(v.Float())), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return reflect.ValueOf(fmt.Sprint(v.Uint())), nil
	case reflect.Bool:
		return reflect.ValueOf(fmt.Sprint(v.Bool())), nil
	}
	if !v.Type().ConvertibleTo(typ) {
		return reflect.Zero(typ), errTypeMismatch(typ, v.Type(), src.GetToken())
	}
	return v.Convert(typ), nil
}

func errTypeMismatch(dstType, srcType reflect.Type, token *token.Token) *errors.TypeError {
	return &errors.TypeError{DstType: dstType, SrcType: srcType, Token: token}
}

type unknownFieldError struct {
	err error
}

func (e *unknownFieldError) Error() string {
	return e.err.Error()
}

func errUnknownField(msg string, tk *token.Token) *unknownFieldError {
	return &unknownFieldError{err: errors.ErrSyntax(msg, tk)}
}

func errUnexpectedNodeType(actual, expected ast.NodeType, tk *token.Token) error {
	return errors.ErrSyntax(fmt.Sprintf("%s was used where %s is expected", actual.YAMLName(), expected.YAMLName()), tk)
}

type duplicateKeyError struct {
	err error
}

func (e *duplicateKeyError) Error() string {
	return e.err.Error()
}

func errDuplicateKey(msg string, tk *token.Token) *duplicateKeyError {
	return &duplicateKeyError{err: errors.ErrSyntax(msg, tk)}
}

func (d *Decoder) deleteStructKeys(structType reflect.Type, unknownFields map[string]ast.Node) error {
	if structType.Kind() == reflect.Ptr {
		structType = structType.Elem()
	}
	structFieldMap, err := structFieldMap(structType)
	if err != nil {
		return err
	}

	for j := 0; j < structType.NumField(); j++ {
		field := structType.Field(j)
		if isIgnoredStructField(field) {
			continue
		}

		structField, exists := structFieldMap[field.Name]
		if !exists {
			continue
		}

		if structField.IsInline {
			_ = d.deleteStructKeys(field.Type, unknownFields)
		} else {
			delete(unknownFields, structField.RenderName)
		}
	}
	return nil
}

func (d *Decoder) lastNode(node ast.Node) ast.Node {
	switch n := node.(type) {
	case *ast.MappingNode:
		if len(n.Values) > 0 {
			return d.lastNode(n.Values[len(n.Values)-1])
		}
	case *ast.MappingValueNode:
		return d.lastNode(n.Value)
	case *ast.SequenceNode:
		if len(n.Values) > 0 {
			return d.lastNode(n.Values[len(n.Values)-1])
		}
	}
	return node
}

func (d *Decoder) unmarshalableDocument(node ast.Node) ([]byte, error) {
	var err error
	node, err = d.resolveAlias(node)
	if err != nil {
		return nil, err
	}
	doc := node.String()
	last := d.lastNode(node)
	if last != nil && last.Type() == ast.LiteralType {
		doc += "\n"
	}
	return []byte(doc), nil
}

func (d *Decoder) unmarshalableText(node ast.Node) ([]byte, bool, error) {
	var err error
	node, err = d.resolveAlias(node)
	if err != nil {
		return nil, false, err
	}
	if node.Type() == ast.AnchorType {
		node = node.(*ast.AnchorNode).Value
	}
	switch n := node.(type) {
	case *ast.StringNode:
		return []byte(n.Value), true, nil
	case *ast.LiteralNode:
		return []byte(n.Value.GetToken().Value), true, nil
	default:
		scalar, ok := n.(ast.ScalarNode)
		if ok {
			return []byte(fmt.Sprint(scalar.GetValue())), true, nil
		}
	}
	return nil, false, nil
}

type jsonUnmarshaler interface {
	UnmarshalJSON([]byte) error
}

func (d *Decoder) existsTypeInCustomUnmarshalerMap(t reflect.Type) bool {
	if _, exists := d.customUnmarshalerMap[t]; exists {
		return true
	}

	globalCustomUnmarshalerMu.Lock()
	defer globalCustomUnmarshalerMu.Unlock()
	if _, exists := globalCustomUnmarshalerMap[t]; exists {
		return true
	}
	return false
}

func (d *Decoder) unmarshalerFromCustomUnmarshalerMap(t reflect.Type) (func(interface{}, []byte) error, bool) {
	if unmarshaler, exists := d.customUnmarshalerMap[t]; exists {
		return unmarshaler, exists
	}

	globalCustomUnmarshalerMu.Lock()
	defer globalCustomUnmarshalerMu.Unlock()
	if unmarshaler, exists := globalCustomUnmarshalerMap[t]; exists {
		return unmarshaler, exists
	}
	return nil, false
}

func (d *Decoder) canDecodeByUnmarshaler(dst reflect.Value) bool {
	ptrValue := dst.Addr()
	if d.existsTypeInCustomUnmarshalerMap(ptrValue.Type()) {
		return true
	}
	iface := ptrValue.Interface()
	switch iface.(type) {
	case BytesUnmarshalerContext:
		return true
	case BytesUnmarshaler:
		return true
	case InterfaceUnmarshalerContext:
		return true
	case InterfaceUnmarshaler:
		return true
	case *time.Time:
		return true
	case *time.Duration:
		return true
	case encoding.TextUnmarshaler:
		return true
	case jsonUnmarshaler:
		return d.useJSONUnmarshaler
	}
	return false
}

func (d *Decoder) decodeByUnmarshaler(ctx context.Context, dst reflect.Value, src ast.Node) error {
	ptrValue := dst.Addr()
	if unmarshaler, exists := d.unmarshalerFromCustomUnmarshalerMap(ptrValue.Type()); exists {
		b, err := d.unmarshalableDocument(src)
		if err != nil {
			return err
		}
		if err := unmarshaler(ptrValue.Interface(), b); err != nil {
			return err
		}
		return nil
	}
	iface := ptrValue.Interface()

	if unmarshaler, ok := iface.(BytesUnmarshalerContext); ok {
		b, err := d.unmarshalableDocument(src)
		if err != nil {
			return err
		}
		if err := unmarshaler.UnmarshalYAML(ctx, b); err != nil {
			return err
		}
		return nil
	}

	if unmarshaler, ok := iface.(BytesUnmarshaler); ok {
		b, err := d.unmarshalableDocument(src)
		if err != nil {
			return err
		}
		if err := unmarshaler.UnmarshalYAML(b); err != nil {
			return err
		}
		return nil
	}

	if unmarshaler, ok := iface.(InterfaceUnmarshalerContext); ok {
		if err := unmarshaler.UnmarshalYAML(ctx, func(v interface{}) error {
			rv := reflect.ValueOf(v)
			if rv.Type().Kind() != reflect.Ptr {
				return ErrDecodeRequiredPointerType
			}
			if err := d.decodeValue(ctx, rv.Elem(), src); err != nil {
				return err
			}
			return nil
		}); err != nil {
			return err
		}
		return nil
	}

	if unmarshaler, ok := iface.(InterfaceUnmarshaler); ok {
		if err := unmarshaler.UnmarshalYAML(func(v interface{}) error {
			rv := reflect.ValueOf(v)
			if rv.Type().Kind() != reflect.Ptr {
				return ErrDecodeRequiredPointerType
			}
			if err := d.decodeValue(ctx, rv.Elem(), src); err != nil {
				return err
			}
			return nil
		}); err != nil {
			return err
		}
		return nil
	}

	if _, ok := iface.(*time.Time); ok {
		return d.decodeTime(ctx, dst, src)
	}

	if _, ok := iface.(*time.Duration); ok {
		return d.decodeDuration(ctx, dst, src)
	}

	if unmarshaler, isText := iface.(encoding.TextUnmarshaler); isText {
		b, ok, err := d.unmarshalableText(src)
		if err != nil {
			return err
		}
		if ok {
			if err := unmarshaler.UnmarshalText(b); err != nil {
				return err
			}
			return nil
		}
	}

	if d.useJSONUnmarshaler {
		if unmarshaler, ok := iface.(jsonUnmarshaler); ok {
			b, err := d.unmarshalableDocument(src)
			if err != nil {
				return err
			}
			jsonBytes, err := YAMLToJSON(b)
			if err != nil {
				return err
			}
			jsonBytes = bytes.TrimRight(jsonBytes, "\n")
			if err := unmarshaler.UnmarshalJSON(jsonBytes); err != nil {
				return err
			}
			return nil
		}
	}

	return fmt.Errorf("does not implemented Unmarshaler")
}

var (
	astNodeType = reflect.TypeOf((*ast.Node)(nil)).Elem()
)

func (d *Decoder) decodeValue(ctx context.Context, dst reflect.Value, src ast.Node) error {
	if src.Type() == ast.AnchorType {
		anchorName := src.(*ast.AnchorNode).Name.GetToken().Value
		if _, exists := d.anchorValueMap[anchorName]; !exists {
			d.anchorValueMap[anchorName] = dst
		}
	}
	if d.canDecodeByUnmarshaler(dst) {
		if err := d.decodeByUnmarshaler(ctx, dst, src); err != nil {
			return err
		}
		return nil
	}
	valueType := dst.Type()
	switch valueType.Kind() {
	case reflect.Ptr:
		if dst.IsNil() {
			return nil
		}
		if src.Type() == ast.NullType {
			// set nil value to pointer
			dst.Set(reflect.Zero(valueType))
			return nil
		}
		v := d.createDecodableValue(dst.Type())
		if err := d.decodeValue(ctx, v, src); err != nil {
			return err
		}
		dst.Set(d.castToAssignableValue(v, dst.Type()))
	case reflect.Interface:
		if dst.Type() == astNodeType {
			dst.Set(reflect.ValueOf(src))
			return nil
		}
		srcVal, err := d.nodeToValue(src)
		if err != nil {
			return err
		}
		v := reflect.ValueOf(srcVal)
		if v.IsValid() {
			dst.Set(v)
		}
	case reflect.Map:
		return d.decodeMap(ctx, dst, src)
	case reflect.Array:
		return d.decodeArray(ctx, dst, src)
	case reflect.Slice:
		if mapSlice, ok := dst.Addr().Interface().(*MapSlice); ok {
			return d.decodeMapSlice(ctx, mapSlice, src)
		}
		return d.decodeSlice(ctx, dst, src)
	case reflect.Struct:
		if mapItem, ok := dst.Addr().Interface().(*MapItem); ok {
			return d.decodeMapItem(ctx, mapItem, src)
		}
		return d.decodeStruct(ctx, dst, src)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v, err := d.nodeToValue(src)
		if err != nil {
			return err
		}
		switch vv := v.(type) {
		case int64:
			if !dst.OverflowInt(vv) {
				dst.SetInt(vv)
				return nil
			}
		case uint64:
			if vv <= math.MaxInt64 && !dst.OverflowInt(int64(vv)) {
				dst.SetInt(int64(vv))
				return nil
			}
		case float64:
			if vv <= math.MaxInt64 && !dst.OverflowInt(int64(vv)) {
				dst.SetInt(int64(vv))
				return nil
			}
		case string: // handle scientific notation
			if i, err := strconv.ParseFloat(vv, 64); err == nil {
				if 0 <= i && i <= math.MaxUint64 && !dst.OverflowInt(int64(i)) {
					dst.SetInt(int64(i))
					return nil
				}
			} else { // couldn't be parsed as float
				return errTypeMismatch(valueType, reflect.TypeOf(v), src.GetToken())
			}
		default:
			return errTypeMismatch(valueType, reflect.TypeOf(v), src.GetToken())
		}
		return errors.ErrOverflow(valueType, fmt.Sprint(v), src.GetToken())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v, err := d.nodeToValue(src)
		if err != nil {
			return err
		}
		switch vv := v.(type) {
		case int64:
			if 0 <= vv && !dst.OverflowUint(uint64(vv)) {
				dst.SetUint(uint64(vv))
				return nil
			}
		case uint64:
			if !dst.OverflowUint(vv) {
				dst.SetUint(vv)
				return nil
			}
		case float64:
			if 0 <= vv && vv <= math.MaxUint64 && !dst.OverflowUint(uint64(vv)) {
				dst.SetUint(uint64(vv))
				return nil
			}
		case string: // handle scientific notation
			if i, err := strconv.ParseFloat(vv, 64); err == nil {
				if 0 <= i && i <= math.MaxUint64 && !dst.OverflowUint(uint64(i)) {
					dst.SetUint(uint64(i))
					return nil
				}
			} else { // couldn't be parsed as float
				return errTypeMismatch(valueType, reflect.TypeOf(v), src.GetToken())
			}

		default:
			return errTypeMismatch(valueType, reflect.TypeOf(v), src.GetToken())
		}
		return errors.ErrOverflow(valueType, fmt.Sprint(v), src.GetToken())
	}
	srcVal, err := d.nodeToValue(src)
	if err != nil {
		return err
	}
	v := reflect.ValueOf(srcVal)
	if v.IsValid() {
		convertedValue, err := d.convertValue(v, dst.Type(), src)
		if err != nil {
			return err
		}
		dst.Set(convertedValue)
	}
	return nil
}

func (d *Decoder) createDecodableValue(typ reflect.Type) reflect.Value {
	for {
		if typ.Kind() == reflect.Ptr {
			typ = typ.Elem()
			continue
		}
		break
	}
	return reflect.New(typ).Elem()
}

func (d *Decoder) castToAssignableValue(value reflect.Value, target reflect.Type) reflect.Value {
	if target.Kind() != reflect.Ptr {
		return value
	}
	maxTryCount := 5
	tryCount := 0
	for {
		if tryCount > maxTryCount {
			return value
		}
		if value.Type().AssignableTo(target) {
			break
		}
		value = value.Addr()
		tryCount++
	}
	return value
}

func (d *Decoder) createDecodedNewValue(
	ctx context.Context, typ reflect.Type, defaultVal reflect.Value, node ast.Node,
) (reflect.Value, error) {
	if node.Type() == ast.AliasType {
		aliasName := node.(*ast.AliasNode).Value.GetToken().Value
		newValue := d.anchorValueMap[aliasName]
		if newValue.IsValid() {
			return newValue, nil
		}
	}
	var newValue reflect.Value
	if node.Type() == ast.NullType {
		newValue = reflect.New(typ).Elem()
	} else {
		newValue = d.createDecodableValue(typ)
	}
	for defaultVal.Kind() == reflect.Ptr {
		defaultVal = defaultVal.Elem()
	}
	if defaultVal.IsValid() && defaultVal.Type().AssignableTo(newValue.Type()) {
		newValue.Set(defaultVal)
	}
	if node.Type() != ast.NullType {
		if err := d.decodeValue(ctx, newValue, node); err != nil {
			return newValue, err
		}
	}
	return newValue, nil
}

func (d *Decoder) keyToNodeMap(node ast.Node, ignoreMergeKey bool, getKeyOrValueNode func(*ast.MapNodeIter) ast.Node) (map[string]ast.Node, error) {
	mapNode, err := d.getMapNode(node)
	if err != nil {
		return nil, err
	}
	keyMap := map[string]struct{}{}
	keyToNodeMap := map[string]ast.Node{}
	if mapNode == nil {
		return keyToNodeMap, nil
	}
	mapIter := mapNode.MapRange()
	for mapIter.Next() {
		keyNode := mapIter.Key()
		if keyNode.Type() == ast.MergeKeyType {
			if ignoreMergeKey {
				continue
			}
			mergeMap, err := d.keyToNodeMap(mapIter.Value(), ignoreMergeKey, getKeyOrValueNode)
			if err != nil {
				return nil, err
			}
			for k, v := range mergeMap {
				if err := d.validateDuplicateKey(keyMap, k, v); err != nil {
					return nil, err
				}
				keyToNodeMap[k] = v
			}
		} else {
			keyVal, err := d.nodeToValue(keyNode)
			if err != nil {
				return nil, err
			}
			key, ok := keyVal.(string)
			if !ok {
				return nil, err
			}
			if err := d.validateDuplicateKey(keyMap, key, keyNode); err != nil {
				return nil, err
			}
			keyToNodeMap[key] = getKeyOrValueNode(mapIter)
		}
	}
	return keyToNodeMap, nil
}

func (d *Decoder) keyToKeyNodeMap(node ast.Node, ignoreMergeKey bool) (map[string]ast.Node, error) {
	m, err := d.keyToNodeMap(node, ignoreMergeKey, func(nodeMap *ast.MapNodeIter) ast.Node { return nodeMap.Key() })
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (d *Decoder) keyToValueNodeMap(node ast.Node, ignoreMergeKey bool) (map[string]ast.Node, error) {
	m, err := d.keyToNodeMap(node, ignoreMergeKey, func(nodeMap *ast.MapNodeIter) ast.Node { return nodeMap.Value() })
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (d *Decoder) setDefaultValueIfConflicted(v reflect.Value, fieldMap StructFieldMap) error {
	typ := v.Type()
	if typ.Kind() != reflect.Struct {
		return nil
	}
	embeddedStructFieldMap, err := structFieldMap(typ)
	if err != nil {
		return err
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if isIgnoredStructField(field) {
			continue
		}
		structField := embeddedStructFieldMap[field.Name]
		if !fieldMap.isIncludedRenderName(structField.RenderName) {
			continue
		}
		// if declared same key name, set default value
		fieldValue := v.Field(i)
		if fieldValue.CanSet() {
			fieldValue.Set(reflect.Zero(fieldValue.Type()))
		}
	}
	return nil
}

// This is a subset of the formats allowed by the regular expression
// defined at http://yaml.org/type/timestamp.html.
var allowedTimestampFormats = []string{
	"2006-1-2T15:4:5.999999999Z07:00", // RCF3339Nano with short date fields.
	"2006-1-2t15:4:5.999999999Z07:00", // RFC3339Nano with short date fields and lower-case "t".
	"2006-1-2 15:4:5.999999999",       // space separated with no time zone
	"2006-1-2",                        // date only
}

func (d *Decoder) castToTime(src ast.Node) (time.Time, error) {
	if src == nil {
		return time.Time{}, nil
	}
	v, err := d.nodeToValue(src)
	if err != nil {
		return time.Time{}, err
	}
	if t, ok := v.(time.Time); ok {
		return t, nil
	}
	s, ok := v.(string)
	if !ok {
		return time.Time{}, errTypeMismatch(reflect.TypeOf(time.Time{}), reflect.TypeOf(v), src.GetToken())
	}
	for _, format := range allowedTimestampFormats {
		t, err := time.Parse(format, s)
		if err != nil {
			// invalid format
			continue
		}
		return t, nil
	}
	return time.Time{}, nil
}

func (d *Decoder) decodeTime(ctx context.Context, dst reflect.Value, src ast.Node) error {
	t, err := d.castToTime(src)
	if err != nil {
		return err
	}
	dst.Set(reflect.ValueOf(t))
	return nil
}

func (d *Decoder) castToDuration(src ast.Node) (time.Duration, error) {
	if src == nil {
		return 0, nil
	}
	v, err := d.nodeToValue(src)
	if err != nil {
		return 0, err
	}
	if t, ok := v.(time.Duration); ok {
		return t, nil
	}
	s, ok := v.(string)
	if !ok {
		return 0, errTypeMismatch(reflect.TypeOf(time.Duration(0)), reflect.TypeOf(v), src.GetToken())
	}
	t, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	return t, nil
}

func (d *Decoder) decodeDuration(ctx context.Context, dst reflect.Value, src ast.Node) error {
	t, err := d.castToDuration(src)
	if err != nil {
		return err
	}
	dst.Set(reflect.ValueOf(t))
	return nil
}

// getMergeAliasName support single alias only
func (d *Decoder) getMergeAliasName(src ast.Node) string {
	mapNode, err := d.getMapNode(src)
	if err != nil {
		return ""
	}
	if mapNode == nil {
		return ""
	}
	mapIter := mapNode.MapRange()
	for mapIter.Next() {
		key := mapIter.Key()
		value := mapIter.Value()
		if key.Type() == ast.MergeKeyType && value.Type() == ast.AliasType {
			return value.(*ast.AliasNode).Value.GetToken().Value
		}
	}
	return ""
}

func (d *Decoder) decodeStruct(ctx context.Context, dst reflect.Value, src ast.Node) error {
	if src == nil {
		return nil
	}
	structType := dst.Type()
	srcValue := reflect.ValueOf(src)
	srcType := srcValue.Type()
	if srcType.Kind() == reflect.Ptr {
		srcType = srcType.Elem()
		srcValue = srcValue.Elem()
	}
	if structType == srcType {
		// dst value implements ast.Node
		dst.Set(srcValue)
		return nil
	}
	structFieldMap, err := structFieldMap(structType)
	if err != nil {
		return err
	}
	ignoreMergeKey := structFieldMap.hasMergeProperty()
	keyToNodeMap, err := d.keyToValueNodeMap(src, ignoreMergeKey)
	if err != nil {
		return err
	}
	var unknownFields map[string]ast.Node
	if d.disallowUnknownField {
		unknownFields, err = d.keyToKeyNodeMap(src, ignoreMergeKey)
		if err != nil {
			return err
		}
	}

	aliasName := d.getMergeAliasName(src)
	var foundErr error

	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)
		if isIgnoredStructField(field) {
			continue
		}
		structField := structFieldMap[field.Name]
		if structField.IsInline {
			fieldValue := dst.FieldByName(field.Name)
			if structField.IsAutoAlias {
				if aliasName != "" {
					newFieldValue := d.anchorValueMap[aliasName]
					if newFieldValue.IsValid() {
						fieldValue.Set(d.castToAssignableValue(newFieldValue, fieldValue.Type()))
					}
				}
				continue
			}
			if !fieldValue.CanSet() {
				return fmt.Errorf("cannot set embedded type as unexported field %s.%s", field.PkgPath, field.Name)
			}
			if fieldValue.Type().Kind() == reflect.Ptr && src.Type() == ast.NullType {
				// set nil value to pointer
				fieldValue.Set(reflect.Zero(fieldValue.Type()))
				continue
			}
			mapNode := ast.Mapping(nil, false)
			for k, v := range keyToNodeMap {
				key := &ast.StringNode{BaseNode: &ast.BaseNode{}, Value: k}
				mapNode.Values = append(mapNode.Values, ast.MappingValue(nil, key, v))
			}
			newFieldValue, err := d.createDecodedNewValue(ctx, fieldValue.Type(), fieldValue, mapNode)
			if d.disallowUnknownField {
				if err := d.deleteStructKeys(fieldValue.Type(), unknownFields); err != nil {
					return err
				}
			}

			if err != nil {
				if foundErr != nil {
					continue
				}
				var te *errors.TypeError
				if errors.As(err, &te) {
					if te.StructFieldName != nil {
						fieldName := fmt.Sprintf("%s.%s", structType.Name(), *te.StructFieldName)
						te.StructFieldName = &fieldName
					} else {
						fieldName := fmt.Sprintf("%s.%s", structType.Name(), field.Name)
						te.StructFieldName = &fieldName
					}
					foundErr = te
					continue
				} else {
					foundErr = err
				}
				continue
			}
			_ = d.setDefaultValueIfConflicted(newFieldValue, structFieldMap)
			fieldValue.Set(d.castToAssignableValue(newFieldValue, fieldValue.Type()))
			continue
		}
		v, exists := keyToNodeMap[structField.RenderName]
		if !exists {
			continue
		}
		delete(unknownFields, structField.RenderName)
		fieldValue := dst.FieldByName(field.Name)
		if fieldValue.Type().Kind() == reflect.Ptr && src.Type() == ast.NullType {
			// set nil value to pointer
			fieldValue.Set(reflect.Zero(fieldValue.Type()))
			continue
		}
		newFieldValue, err := d.createDecodedNewValue(ctx, fieldValue.Type(), fieldValue, v)
		if err != nil {
			if foundErr != nil {
				continue
			}
			var te *errors.TypeError
			if errors.As(err, &te) {
				fieldName := fmt.Sprintf("%s.%s", structType.Name(), field.Name)
				te.StructFieldName = &fieldName
				foundErr = te
			} else {
				foundErr = err
			}
			continue
		}
		fieldValue.Set(d.castToAssignableValue(newFieldValue, fieldValue.Type()))
	}
	if foundErr != nil {
		return foundErr
	}

	// Ignore unknown fields when parsing an inline struct (recognized by a nil token).
	// Unknown fields are expected (they could be fields from the parent struct).
	if len(unknownFields) != 0 && d.disallowUnknownField && src.GetToken() != nil {
		for key, node := range unknownFields {
			return errUnknownField(fmt.Sprintf(`unknown field "%s"`, key), node.GetToken())
		}
	}

	if d.validator != nil {
		if err := d.validator.Struct(dst.Interface()); err != nil {
			ev := reflect.ValueOf(err)
			if ev.Type().Kind() == reflect.Slice {
				for i := 0; i < ev.Len(); i++ {
					fieldErr, ok := ev.Index(i).Interface().(FieldError)
					if !ok {
						continue
					}
					fieldName := fieldErr.StructField()
					structField, exists := structFieldMap[fieldName]
					if !exists {
						continue
					}
					node, exists := keyToNodeMap[structField.RenderName]
					if exists {
						// TODO: to make FieldError message cutomizable
						return errors.ErrSyntax(fmt.Sprintf("%s", err), node.GetToken())
					} else if t := src.GetToken(); t != nil && t.Prev != nil && t.Prev.Prev != nil {
						// A missing required field will not be in the keyToNodeMap
						// the error needs to be associated with the parent of the source node
						return errors.ErrSyntax(fmt.Sprintf("%s", err), t.Prev.Prev)
					}
				}
			}
			return err
		}
	}
	return nil
}

func (d *Decoder) decodeArray(ctx context.Context, dst reflect.Value, src ast.Node) error {
	arrayNode, err := d.getArrayNode(src)
	if err != nil {
		return err
	}
	if arrayNode == nil {
		return nil
	}
	iter := arrayNode.ArrayRange()
	arrayValue := reflect.New(dst.Type()).Elem()
	arrayType := dst.Type()
	elemType := arrayType.Elem()
	idx := 0

	var foundErr error
	for iter.Next() {
		v := iter.Value()
		if elemType.Kind() == reflect.Ptr && v.Type() == ast.NullType {
			// set nil value to pointer
			arrayValue.Index(idx).Set(reflect.Zero(elemType))
		} else {
			dstValue, err := d.createDecodedNewValue(ctx, elemType, reflect.Value{}, v)
			if err != nil {
				if foundErr == nil {
					foundErr = err
				}
				continue
			} else {
				arrayValue.Index(idx).Set(d.castToAssignableValue(dstValue, elemType))
			}
		}
		idx++
	}
	dst.Set(arrayValue)
	if foundErr != nil {
		return foundErr
	}
	return nil
}

func (d *Decoder) decodeSlice(ctx context.Context, dst reflect.Value, src ast.Node) error {
	arrayNode, err := d.getArrayNode(src)
	if err != nil {
		return err
	}
	if arrayNode == nil {
		return nil
	}
	iter := arrayNode.ArrayRange()
	sliceType := dst.Type()
	sliceValue := reflect.MakeSlice(sliceType, 0, iter.Len())
	elemType := sliceType.Elem()

	var foundErr error
	for iter.Next() {
		v := iter.Value()
		if elemType.Kind() == reflect.Ptr && v.Type() == ast.NullType {
			// set nil value to pointer
			sliceValue = reflect.Append(sliceValue, reflect.Zero(elemType))
			continue
		}
		dstValue, err := d.createDecodedNewValue(ctx, elemType, reflect.Value{}, v)
		if err != nil {
			if foundErr == nil {
				foundErr = err
			}
			continue
		}
		sliceValue = reflect.Append(sliceValue, d.castToAssignableValue(dstValue, elemType))
	}
	dst.Set(sliceValue)
	if foundErr != nil {
		return foundErr
	}
	return nil
}

func (d *Decoder) decodeMapItem(ctx context.Context, dst *MapItem, src ast.Node) error {
	mapNode, err := d.getMapNode(src)
	if err != nil {
		return err
	}
	if mapNode == nil {
		return nil
	}
	mapIter := mapNode.MapRange()
	if !mapIter.Next() {
		return nil
	}
	key := mapIter.Key()
	value := mapIter.Value()
	if key.Type() == ast.MergeKeyType {
		if err := d.decodeMapItem(ctx, dst, value); err != nil {
			return err
		}
		return nil
	}
	k, err := d.nodeToValue(key)
	if err != nil {
		return err
	}
	v, err := d.nodeToValue(value)
	if err != nil {
		return err
	}
	*dst = MapItem{Key: k, Value: v}
	return nil
}

func (d *Decoder) validateDuplicateKey(keyMap map[string]struct{}, key interface{}, keyNode ast.Node) error {
	k, ok := key.(string)
	if !ok {
		return nil
	}
	if d.disallowDuplicateKey {
		if _, exists := keyMap[k]; exists {
			return errDuplicateKey(fmt.Sprintf(`duplicate key "%s"`, k), keyNode.GetToken())
		}
	}
	keyMap[k] = struct{}{}
	return nil
}

func (d *Decoder) decodeMapSlice(ctx context.Context, dst *MapSlice, src ast.Node) error {
	mapNode, err := d.getMapNode(src)
	if err != nil {
		return err
	}
	if mapNode == nil {
		return nil
	}
	mapSlice := MapSlice{}
	mapIter := mapNode.MapRange()
	keyMap := map[string]struct{}{}
	for mapIter.Next() {
		key := mapIter.Key()
		value := mapIter.Value()
		if key.Type() == ast.MergeKeyType {
			var m MapSlice
			if err := d.decodeMapSlice(ctx, &m, value); err != nil {
				return err
			}
			for _, v := range m {
				if err := d.validateDuplicateKey(keyMap, v.Key, value); err != nil {
					return err
				}
				mapSlice = append(mapSlice, v)
			}
			continue
		}
		k, err := d.nodeToValue(key)
		if err != nil {
			return err
		}
		if err := d.validateDuplicateKey(keyMap, k, key); err != nil {
			return err
		}
		v, err := d.nodeToValue(value)
		if err != nil {
			return err
		}
		mapSlice = append(mapSlice, MapItem{Key: k, Value: v})
	}
	*dst = mapSlice
	return nil
}

func (d *Decoder) decodeMap(ctx context.Context, dst reflect.Value, src ast.Node) error {
	mapNode, err := d.getMapNode(src)
	if err != nil {
		return err
	}
	if mapNode == nil {
		return nil
	}
	mapType := dst.Type()
	mapValue := reflect.MakeMap(mapType)
	keyType := mapValue.Type().Key()
	valueType := mapValue.Type().Elem()
	mapIter := mapNode.MapRange()
	keyMap := map[string]struct{}{}
	var foundErr error
	for mapIter.Next() {
		key := mapIter.Key()
		value := mapIter.Value()
		if key.Type() == ast.MergeKeyType {
			if err := d.decodeMap(ctx, dst, value); err != nil {
				return err
			}
			iter := dst.MapRange()
			for iter.Next() {
				if err := d.validateDuplicateKey(keyMap, iter.Key(), value); err != nil {
					return err
				}
				mapValue.SetMapIndex(iter.Key(), iter.Value())
			}
			continue
		}

		k := d.createDecodableValue(keyType)
		if d.canDecodeByUnmarshaler(k) {
			if err := d.decodeByUnmarshaler(ctx, k, key); err != nil {
				return err
			}
		} else {
			keyVal, err := d.nodeToValue(key)
			if err != nil {
				return err
			}
			k = reflect.ValueOf(keyVal)
			if k.IsValid() && k.Type().ConvertibleTo(keyType) {
				k = k.Convert(keyType)
			}
		}

		if k.IsValid() {
			if err := d.validateDuplicateKey(keyMap, k.Interface(), key); err != nil {
				return err
			}
		}
		if valueType.Kind() == reflect.Ptr && value.Type() == ast.NullType {
			// set nil value to pointer
			mapValue.SetMapIndex(k, reflect.Zero(valueType))
			continue
		}
		dstValue, err := d.createDecodedNewValue(ctx, valueType, reflect.Value{}, value)
		if err != nil {
			if foundErr == nil {
				foundErr = err
			}
		}
		if !k.IsValid() {
			// expect nil key
			mapValue.SetMapIndex(d.createDecodableValue(keyType), d.castToAssignableValue(dstValue, valueType))
			continue
		}
		mapValue.SetMapIndex(k, d.castToAssignableValue(dstValue, valueType))
	}
	dst.Set(mapValue)
	if foundErr != nil {
		return foundErr
	}
	return nil
}

func (d *Decoder) fileToReader(file string) (io.Reader, error) {
	reader, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	return reader, nil
}

func (d *Decoder) isYAMLFile(file string) bool {
	ext := filepath.Ext(file)
	if ext == ".yml" {
		return true
	}
	if ext == ".yaml" {
		return true
	}
	return false
}

func (d *Decoder) readersUnderDir(dir string) ([]io.Reader, error) {
	pattern := fmt.Sprintf("%s/*", dir)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	readers := []io.Reader{}
	for _, match := range matches {
		if !d.isYAMLFile(match) {
			continue
		}
		reader, err := d.fileToReader(match)
		if err != nil {
			return nil, err
		}
		readers = append(readers, reader)
	}
	return readers, nil
}

func (d *Decoder) readersUnderDirRecursive(dir string) ([]io.Reader, error) {
	readers := []io.Reader{}
	if err := filepath.Walk(dir, func(path string, info os.FileInfo, _ error) error {
		if !d.isYAMLFile(path) {
			return nil
		}
		reader, readerErr := d.fileToReader(path)
		if readerErr != nil {
			return readerErr
		}
		readers = append(readers, reader)
		return nil
	}); err != nil {
		return nil, err
	}
	return readers, nil
}

func (d *Decoder) resolveReference() error {
	for _, opt := range d.opts {
		if err := opt(d); err != nil {
			return err
		}
	}
	for _, file := range d.referenceFiles {
		reader, err := d.fileToReader(file)
		if err != nil {
			return err
		}
		d.referenceReaders = append(d.referenceReaders, reader)
	}
	for _, dir := range d.referenceDirs {
		if !d.isRecursiveDir {
			readers, err := d.readersUnderDir(dir)
			if err != nil {
				return err
			}
			d.referenceReaders = append(d.referenceReaders, readers...)
		} else {
			readers, err := d.readersUnderDirRecursive(dir)
			if err != nil {
				return err
			}
			d.referenceReaders = append(d.referenceReaders, readers...)
		}
	}
	for _, reader := range d.referenceReaders {
		bytes, err := io.ReadAll(reader)
		if err != nil {
			return err
		}

		// assign new anchor definition to anchorMap
		if _, err := d.parse(bytes); err != nil {
			return err
		}
	}
	d.isResolvedReference = true
	return nil
}

func (d *Decoder) parse(bytes []byte) (*ast.File, error) {
	var parseMode parser.Mode
	if d.toCommentMap != nil {
		parseMode = parser.ParseComments
	}
	f, err := parser.ParseBytes(bytes, parseMode)
	if err != nil {
		return nil, err
	}
	normalizedFile := &ast.File{}
	for _, doc := range f.Docs {
		// try to decode ast.Node to value and map anchor value to anchorMap
		v, err := d.nodeToValue(doc.Body)
		if err != nil {
			return nil, err
		}
		if v != nil {
			normalizedFile.Docs = append(normalizedFile.Docs, doc)
		}
	}
	return normalizedFile, nil
}

func (d *Decoder) isInitialized() bool {
	return d.parsedFile != nil
}

func (d *Decoder) decodeInit() error {
	if !d.isResolvedReference {
		if err := d.resolveReference(); err != nil {
			return err
		}
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, d.reader); err != nil {
		return err
	}
	file, err := d.parse(buf.Bytes())
	if err != nil {
		return err
	}
	d.parsedFile = file
	return nil
}

func (d *Decoder) decode(ctx context.Context, v reflect.Value) error {
	if len(d.parsedFile.Docs) <= d.streamIndex {
		return io.EOF
	}
	body := d.parsedFile.Docs[d.streamIndex].Body
	if body == nil {
		return nil
	}
	if err := d.decodeValue(ctx, v.Elem(), body); err != nil {
		return err
	}
	d.streamIndex++
	return nil
}

// Decode reads the next YAML-encoded value from its input
// and stores it in the value pointed to by v.
//
// See the documentation for Unmarshal for details about the
// conversion of YAML into a Go value.
func (d *Decoder) Decode(v interface{}) error {
	return d.DecodeContext(context.Background(), v)
}

// DecodeContext reads the next YAML-encoded value from its input
// and stores it in the value pointed to by v with context.Context.
func (d *Decoder) DecodeContext(ctx context.Context, v interface{}) error {
	rv := reflect.ValueOf(v)
	if rv.Type().Kind() != reflect.Ptr {
		return ErrDecodeRequiredPointerType
	}
	if d.isInitialized() {
		if err := d.decode(ctx, rv); err != nil {
			if err == io.EOF {
				return err
			}
			return err
		}
		return nil
	}
	if err := d.decodeInit(); err != nil {
		return err
	}
	if err := d.decode(ctx, rv); err != nil {
		if err == io.EOF {
			return err
		}
		return err
	}
	return nil
}

// DecodeFromNode decodes node into the value pointed to by v.
func (d *Decoder) DecodeFromNode(node ast.Node, v interface{}) error {
	return d.DecodeFromNodeContext(context.Background(), node, v)
}

// DecodeFromNodeContext decodes node into the value pointed to by v with context.Context.
func (d *Decoder) DecodeFromNodeContext(ctx context.Context, node ast.Node, v interface{}) error {
	rv := reflect.ValueOf(v)
	if rv.Type().Kind() != reflect.Ptr {
		return ErrDecodeRequiredPointerType
	}
	if !d.isInitialized() {
		if err := d.decodeInit(); err != nil {
			return err
		}
	}
	// resolve references to the anchor on the same file
	if _, err := d.nodeToValue(node); err != nil {
		return err
	}
	if err := d.decodeValue(ctx, rv.Elem(), node); err != nil {
		return err
	}
	return nil
}
