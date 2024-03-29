package main

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type namePair struct {
	input  string
	output string
}

func main() {
	var mappings []namePair
	var filename string
	var goImportPath string

	protogen.Options{
		ParamFunc: func(name, value string) error {
			switch name {
			case "mapping":
				parts := strings.Split(value, ":")
				if len(parts) != 2 {
					return fmt.Errorf("invalid mapping \"%q\". Must be of the form input:output", value)
				}
				mappings = append(mappings, namePair{parts[0], parts[1]})
				return nil
			case "filename":
				filename = value
				return nil
			case "go_import_path":
				goImportPath = value
				return nil
			}
			return fmt.Errorf("unknown parameter %q", name)
		},
	}.Run(func(gen *protogen.Plugin) error {
		messageMap, enumMap := mapFiles(gen)
		outFile := gen.NewGeneratedFile(filename, protogen.GoImportPath(goImportPath))

		outFile.P("// Code generated by protoc-gen-encode. DO NOT EDIT.")
		outFile.P()
		outFile.P("package ", reverseString(strings.Split(reverseString(goImportPath), "/")[0]))

		for _, mapping := range mappings {
			inMsg, inOk := messageMap[mapping.input]
			outMsg, outOk := messageMap[mapping.output]
			if inOk && !outOk {
				return fmt.Errorf("output message %q not found", mapping.output)
			}
			if !inOk && outOk {
				return fmt.Errorf("input message %q not found", mapping.input)
			}
			if inOk && outOk {
				err := convertMessage(inMsg, outMsg, outFile)
				if err != nil {
					return err
				}
			}

			inEnum, inOk := enumMap[mapping.input]
			outEnum, outOk := enumMap[mapping.output]
			if inOk && !outOk {
				return fmt.Errorf("output enum %q not found", mapping.output)
			}
			if !inOk && outOk {
				return fmt.Errorf("input enum %q not found", mapping.input)
			}
			if inOk && outOk {
				err := convertEnum(inEnum, outEnum, outFile)
				if err != nil {
					return err
				}
			}
		}

		return nil
	})
}

func convertMessage(inMsg *protogen.Message, outMsg *protogen.Message, g *protogen.GeneratedFile) error {
	inFields := map[string]*protogen.Field{}
	for _, field := range inMsg.Fields {
		inFields[field.GoName] = field
	}

	outFields := map[string]*protogen.Field{}
	extraFields := map[string]*protogen.Field{}
	changedFields := map[string]*protogen.Field{}
	commonFields := map[string]*protogen.Field{}
	for _, field := range outMsg.Fields {
		outFields[field.GoName] = field
		inField, ok := inFields[field.GoName]
		if !ok {
			extraFields[field.GoName] = field
			continue
		}
		// We pass in false for input for both because we care about
		// pointers.
		inType := fieldGoType(g, inField)
		outType := fieldGoType(g, field)
		if inType != outType {
			changedFields[field.GoName] = field
			continue
		}

		commonFields[field.GoName] = field
	}

	missingFields := map[string]*protogen.Field{}
	for _, field := range inMsg.Fields {
		if _, ok := outFields[field.GoName]; !ok {
			missingFields[field.GoName] = field
		}
	}

	for _, field := range inMsg.Fields {
		if _, ok := changedFields[field.GoName]; ok {
			inFieldType := fieldGoType(g, field)
			outFieldType := fieldGoType(g, outFields[field.GoName])

			g.P("type ", fieldEncoderName(g, outMsg, field), "[T any]", "func(", inFieldType, ", T) (", outFieldType, ", error)")
			g.P()
		}
		if _, ok := missingFields[field.GoName]; ok {
			g.P("type ", ackMissingName(g, outMsg, field), " struct {}")
			g.P()
		}
	}

	for _, field := range outMsg.Fields {
		if _, ok := extraFields[field.GoName]; !ok {
			continue
		}

		fieldType := fieldGoType(g, field)
		g.P("type ", fieldEncoderName(g, outMsg, field), "[T any]", "func(T) (", fieldType, ", error)")
		g.P()
	}

	g.P("type ", encoderName(g, inMsg, outMsg), "[T any] struct {")

	for _, field := range inMsg.Fields {
		if _, ok := changedFields[field.GoName]; ok {
			g.P(field.GoName, " ", fieldEncoderName(g, outMsg, field), "[T]")
			continue
		}
		if outField, ok := outFields[field.GoName]; ok {
			inFieldType := fieldGoType(g, field)
			outFieldType := fieldGoType(g, outField)
			g.P(field.GoName, " func(", inFieldType, ", T) (", outFieldType, ", error)")
		}
	}

	for _, field := range outMsg.Fields {
		if _, ok := extraFields[field.GoName]; !ok {
			continue
		}

		g.P(field.GoName, " ", fieldEncoderName(g, outMsg, field), "[T]")
	}

	g.P("}")
	g.P()

	g.P("func New", encoderName(g, inMsg, outMsg), "[T any](")
	for _, field := range inMsg.Fields {
		if _, ok := changedFields[field.GoName]; ok {
			g.P(field.GoName, " ", fieldEncoderName(g, outMsg, field), "[T],")
		}
		if _, ok := missingFields[field.GoName]; ok {
			g.P(field.GoName, " ", ackMissingName(g, outMsg, field), ",")
		}
	}

	for _, field := range outMsg.Fields {
		if _, ok := extraFields[field.GoName]; !ok {
			continue
		}
		g.P(field.GoName, " ", fieldEncoderName(g, outMsg, field), "[T],")
	}

	g.P(") *", encoderName(g, inMsg, outMsg), "[T] {")

	g.P("return &", encoderName(g, inMsg, outMsg), "[T]{")

	for _, field := range outMsg.Fields {
		if _, ok := commonFields[field.GoName]; ok {
			inFieldType := fieldGoType(g, field)
			outFieldType := fieldGoType(g, field)

			g.P(field.GoName, ": ", "func(in ", inFieldType, ", extra T) (", outFieldType, ", error) { return in, nil },")
		}

		if _, ok := changedFields[field.GoName]; ok {
			g.P(field.GoName, ": ", field.GoName, ",")
		}

		if _, ok := extraFields[field.GoName]; ok {
			g.P(field.GoName, ": ", field.GoName, ",")
		}
	}

	g.P("}")

	g.P("}")
	g.P()

	g.P("func (e *", encoderName(g, inMsg, outMsg), "[T]) Encode(in *", g.QualifiedGoIdent(inMsg.GoIdent), ", extra T) (*", g.QualifiedGoIdent(outMsg.GoIdent), ", error) {")

	g.P("var err error")
	// I would have liked to have set to variables and then constructed out
	// all at once, but that doesn't work for oneof fields because the type
	// is a private interface.
	g.P("out := &", g.QualifiedGoIdent(outMsg.GoIdent), "{}")
	g.P()

	for _, outField := range outMsg.Fields {
		inField, ok := inFields[outField.GoName]

		if ok {
			if inField.Oneof == nil {
				if outField.Oneof != nil {
					g.P(lowerFirst(inField.GoName), ", err := e.", inField.GoName, "(in.", inField.GoName, ", extra)")
					g.P("if err != nil {")
					g.P("return nil, err")
					g.P("}")
					g.P("if ", lowerFirst(inField.GoName), " != nil {")
					g.P("out.", outField.Oneof.GoName, " = &", g.QualifiedGoIdent(outField.GoIdent), "{")

					indirect := ""
					if _, ptr := fieldGoTypePointer(g, outField); ptr {
						indirect = "*"
					}
					g.P(inField.GoName, ": ", indirect, lowerFirst(inField.GoName), ",")
					g.P("}")
					g.P("}")
				} else {
					g.P("out.", inField.GoName, ",  err = e.", inField.GoName, "(in.", inField.GoName, ", extra)")
					g.P("if err != nil {")
					g.P("return nil, err")
					g.P("}")
				}
				g.P()
			}

			if inField.Oneof != nil {
				reference := ""
				// indirect := ""
				if inField.Desc.Kind() != protoreflect.MessageKind {
					reference = "&"
					// indirect = "*"
				}
				g.P("var ", lowerFirst(inField.GoName), "In ", fieldGoType(g, inField))
				g.P("if in.", inField.Oneof.GoName, " != nil {")
				g.P("if t, ok := in.", inField.Oneof.GoName, ".(*", g.QualifiedGoIdent(inField.GoIdent), "); ok {")

				g.P(lowerFirst(inField.GoName), "In = ", reference, "t.", inField.GoName)
				g.P("}")
				g.P("}")
				if outField.Oneof != nil {
					g.P(lowerFirst(inField.GoName), ", err := e.", inField.GoName, "(", lowerFirst(inField.GoName), "In, extra)")
					g.P("if err != nil {")
					g.P("return nil, err")
					g.P("}")
					g.P("if ", lowerFirst(inField.GoName), " != nil {")
					g.P("out.", outField.Oneof.GoName, " = &", g.QualifiedGoIdent(outField.GoIdent), "{")
					indirect := ""
					if _, ptr := fieldGoTypePointer(g, outField); ptr {
						indirect = "*"
					}
					g.P(inField.GoName, ": ", indirect, lowerFirst(inField.GoName), ",")
					g.P("}")
					g.P("}")

				} else {
					g.P("out.", inField.GoName, ", err = e.", inField.GoName, "(", lowerFirst(inField.GoName), "In, extra)")
					g.P("if err != nil {")
					g.P("return nil, err")
					g.P("}")
				}

				g.P()
			}

		} else {

			if outField.Oneof != nil {
				g.P(lowerFirst(outField.GoName), ", err := e.", outField.GoName, "(extra)")
				g.P("if err != nil {")
				g.P("return nil, err")
				g.P("}")
				g.P("if ", lowerFirst(outField.GoName), " != nil {")
				g.P("out.", outField.Oneof.GoName, " = &", g.QualifiedGoIdent(outField.GoIdent), "{")
				indirect := ""
				if _, ptr := fieldGoTypePointer(g, outField); ptr {
					indirect = "*"
				}
				g.P(outField.GoName, ": ", indirect, lowerFirst(outField.GoName), ",")
				g.P("}")
				g.P("}")
			} else {
				g.P("out.", outField.GoName, ", err = e.", outField.GoName, "(extra)")
				g.P("if err != nil {")
				g.P("return nil, err")
				g.P("}")
			}
			g.P()
		}
	}

	g.P("return out, nil")
	g.P("}")
	return nil
}

func convertEnum(inEnum *protogen.Enum, outEnum *protogen.Enum, g *protogen.GeneratedFile) error {
	inFields := map[string]*protogen.EnumValue{}
	for _, field := range inEnum.Values {
		inFields[string(field.Desc.Name())] = field
	}

	outFields := map[string]*protogen.EnumValue{}
	extraFields := map[string]*protogen.EnumValue{}
	commonFields := map[string]*protogen.EnumValue{}
	for _, field := range outEnum.Values {
		outFields[string(field.Desc.Name())] = field
		_, ok := inFields[string(field.Desc.Name())]
		if !ok {
			extraFields[string(field.Desc.Name())] = field
			continue
		}
		commonFields[string(field.Desc.Name())] = field
	}

	missingFields := map[string]*protogen.EnumValue{}
	for _, field := range inEnum.Values {
		if _, ok := outFields[string(field.Desc.Name())]; !ok {
			missingFields[string(field.Desc.Name())] = field
		}
	}

	for _, field := range inEnum.Values {
		if _, ok := missingFields[string(field.Desc.Name())]; ok {
			g.P("type ", ackMissingEnumName(g, outEnum, field), " struct {}")
			g.P()
		}
	}

	g.P("type ", "Extra", upperFirst(encoderEnumName(g, inEnum, outEnum)), "[T any] func(T) (*", g.QualifiedGoIdent(outEnum.GoIdent), ", error)")

	g.P("type ", encoderEnumName(g, inEnum, outEnum), "[T any] struct {")
	g.P("ExtraEncoder Extra", upperFirst(encoderEnumName(g, inEnum, outEnum)), "[T]")
	g.P("}")

	g.P("func New", encoderEnumName(g, inEnum, outEnum), "[T any](")
	g.P("extraEncoder Extra", upperFirst(encoderEnumName(g, inEnum, outEnum)), "[T],")
	for _, field := range inEnum.Values {
		if _, ok := missingFields[string(field.Desc.Name())]; ok {
			g.P(string(field.Desc.Name()), " ", ackMissingEnumName(g, outEnum, field), ",")
		}
	}
	g.P(") *", encoderEnumName(g, inEnum, outEnum), "[T] {")
	g.P("return &", encoderEnumName(g, inEnum, outEnum), "[T]{")
	g.P("ExtraEncoder: extraEncoder,")
	g.P("}")
	g.P("}")
	g.P()

	g.P("func (e *", encoderEnumName(g, inEnum, outEnum), "[T]) Encode(in ", g.QualifiedGoIdent(inEnum.GoIdent), ", extra T) (", g.QualifiedGoIdent(outEnum.GoIdent), ", error) {")
	g.P("var err error")
	g.P("var out ", g.QualifiedGoIdent(outEnum.GoIdent))
	for _, field := range outEnum.Values {
		if inField, ok := inFields[string(field.Desc.Name())]; ok {
			g.P("if in == ", g.QualifiedGoIdent(inField.GoIdent), " {")
			g.P("out = ", g.QualifiedGoIdent(field.GoIdent))
			g.P("}")
			g.P()
		}
	}

	g.P("var extraOut *", g.QualifiedGoIdent(outEnum.GoIdent))
	g.P("extraOut, err = e.ExtraEncoder(extra)")
	g.P("if err != nil {")
	g.P("return 0, err")
	g.P("}")
	g.P()

	g.P("if extraOut != nil {")
	g.P("out = *extraOut")
	g.P("}")
	g.P()

	g.P("return out, nil")
	g.P("}")

	return nil
}

func mapFiles(gen *protogen.Plugin) (map[string]*protogen.Message, map[string]*protogen.Enum) {
	messageMap := map[string]*protogen.Message{}
	enumMap := map[string]*protogen.Enum{}
	for _, file := range gen.Files {
		mapCollection(fileCollection{file}, messageMap, enumMap)
	}
	return messageMap, enumMap
}

func mapCollection(c messageEnumCollection, messageMap map[string]*protogen.Message, enumMap map[string]*protogen.Enum) {
	for _, message := range c.Messages() {
		messageMap[string(message.Desc.FullName())] = message
		mapCollection(messageCollection{message}, messageMap, enumMap)
	}

	for _, enum := range c.Enums() {
		enumMap[string(enum.Desc.FullName())] = enum
	}
}

type messageEnumCollection interface {
	Messages() []*protogen.Message
	Enums() []*protogen.Enum
}

type fileCollection struct {
	file *protogen.File
}

func (f fileCollection) Messages() []*protogen.Message {
	return f.file.Messages
}

func (f fileCollection) Enums() []*protogen.Enum {
	return f.file.Enums
}

type messageCollection struct {
	message *protogen.Message
}

func (m messageCollection) Messages() []*protogen.Message {
	return m.message.Messages
}

func (m messageCollection) Enums() []*protogen.Enum {
	return m.message.Enums
}

func fieldGoType(g *protogen.GeneratedFile, field *protogen.Field) string {
	goType, pointer := fieldGoTypePointer(g, field)
	if pointer {
		return "*" + goType
	}
	return goType
}

func fieldGoTypePointer(g *protogen.GeneratedFile, field *protogen.Field) (goType string, pointer bool) {
	if field.Desc.IsWeak() {
		return "struct{}", false
	}

	pointer = field.Desc.HasPresence()
	switch field.Desc.Kind() {
	case protoreflect.BoolKind:
		goType = "bool"
	case protoreflect.EnumKind:
		goType = g.QualifiedGoIdent(field.Enum.GoIdent)
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		goType = "int32"
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		goType = "uint32"
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		goType = "int64"
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		goType = "uint64"
	case protoreflect.FloatKind:
		goType = "float32"
	case protoreflect.DoubleKind:
		goType = "float64"
	case protoreflect.StringKind:
		goType = "string"
	case protoreflect.BytesKind:
		goType = "[]byte"
		pointer = false // rely on nullability of slices for presence
	case protoreflect.MessageKind, protoreflect.GroupKind:
		goType = "*" + g.QualifiedGoIdent(field.Message.GoIdent)
		pointer = false // pointer captured as part of the type
	}
	switch {
	case field.Desc.IsList():
		return "[]" + goType, false
	case field.Desc.IsMap():
		keyType, _ := fieldGoTypePointer(g, field.Message.Fields[0])
		valType, _ := fieldGoTypePointer(g, field.Message.Fields[1])
		return fmt.Sprintf("map[%v]%v", keyType, valType), false
	}
	return goType, pointer
}

func reverseString(s string) string {
	size := len(s)
	buf := make([]byte, size)
	for start := 0; start < size; {
		r, n := utf8.DecodeRuneInString(s[start:])
		start += n
		utf8.EncodeRune(buf[size-start:], r)
	}
	return string(buf)
}

func lowerFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError || size < 1 {
		return s
	}
	lc := unicode.ToLower(r)
	if lc == r {
		return s
	}
	return string(lc) + s[size:]
}

func upperFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError || size < 1 {
		return s
	}
	uc := unicode.ToUpper(r)
	if uc == r {
		return s
	}
	return string(uc) + s[size:]
}

func uniqueName(g *protogen.GeneratedFile, ident protogen.GoIdent) string {
	s := g.QualifiedGoIdent(ident)
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return s
	}
	return fmt.Sprintf("%s%s", upperFirst(parts[0]), parts[1])
}

func encoderName(g *protogen.GeneratedFile, inMsg, outMsg *protogen.Message) string {
	return fmt.Sprintf("%sTo%sEncoder", uniqueName(g, inMsg.GoIdent), uniqueName(g, outMsg.GoIdent))
}

func encoderEnumName(g *protogen.GeneratedFile, inEnum, outEnum *protogen.Enum) string {
	return fmt.Sprintf("%sTo%sEncoder", uniqueName(g, inEnum.GoIdent), uniqueName(g, outEnum.GoIdent))
}

func fieldEncoderName(g *protogen.GeneratedFile, msg *protogen.Message, field *protogen.Field) string {
	return fmt.Sprintf("%s%sEncoder", uniqueName(g, msg.GoIdent), upperFirst(field.GoName))
}

func fieldEncoderEnumName(g *protogen.GeneratedFile, enum *protogen.Enum, field *protogen.EnumValue) string {
	return fmt.Sprintf("%s%sEncoder", uniqueName(g, enum.GoIdent), upperFirst(field.GoIdent.GoName))
}

func ackMissingName(g *protogen.GeneratedFile, msg *protogen.Message, field *protogen.Field) string {
	return fmt.Sprintf("%s%sAckMissing", uniqueName(g, msg.GoIdent), upperFirst(field.GoName))
}

func ackMissingEnumName(g *protogen.GeneratedFile, enum *protogen.Enum, field *protogen.EnumValue) string {
	return fmt.Sprintf("%s%sAckMissing", uniqueName(g, enum.GoIdent), upperFirst(field.GoIdent.GoName))
}
