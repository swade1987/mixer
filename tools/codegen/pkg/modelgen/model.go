// Copyright 2017 Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package modelgen

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	proto "github.com/gogo/protobuf/proto"
	"github.com/gogo/protobuf/protoc-gen-gogo/descriptor"

	tmpl "istio.io/mixer/pkg/adapter/template"
)

const fullProtoNameOfValueTypeEnum = "istio.mixer.v1.config.descriptor.ValueType"

//var SupportedCustomMessageTypes = map[string]string{
//	"google.protobuf.Timestamp": "",
//	"google.protobuf.Duration":  "",
//}

type (
	// Model represents the object used to code generate mixer artifacts.
	Model struct {
		Name        string
		VarietyName string

		PackageImportPath string

		Comment string

		// Info for go interfaces
		GoPackageName string

		// Info for regenerated template proto
		PackageName     string
		TemplateMessage MessageInfo

		// Warnings/Errors in the Template proto file.
		diags []diag
	}

	// FieldInfo contains the data about the field
	FieldInfo struct {
		ProtoName string
		GoName    string
		Number    string // Only for proto fields
		Comment   string

		ProtoType TypeInfo
		GoType    TypeInfo
	}

	// TypeInfo contains the data about the field
	TypeInfo struct {
		Name        string
		IsRepeated  bool
		IsMap       bool
		IsValueType bool
		MapKey      *TypeInfo
		MapValue    *TypeInfo
	}

	// MessageInfo contains the data about the type/message
	MessageInfo struct {
		Comment string
		Fields  []FieldInfo
	}
)

// Create creates a Model object.
func Create(parser *FileDescriptorSetParser) (*Model, error) {

	templateProto, diags := getTmplFileDesc(parser.allFiles)
	if len(diags) != 0 {
		return nil, createGoError(diags)
	}

	// set the current generated code package to the package of the
	// templateProto. This will make sure references within the
	// generated file into the template's pb.go file are fully qualified.
	parser.packageName = goPackageName(templateProto.GetPackage())

	model := &Model{diags: make([]diag, 0)}

	model.fillModel(templateProto, parser)
	if len(model.diags) > 0 {
		return nil, createGoError(model.diags)
	}

	return model, nil
}

func createGoError(diags []diag) error {
	return fmt.Errorf("errors during parsing:\n%s", stringifyDiags(diags))
}

func (m *Model) fillModel(templateProto *FileDescriptor, parser *FileDescriptorSetParser) {

	m.PackageImportPath = parser.PackageImportPath

	m.addTopLevelFields(templateProto)
	// ensure Template is present
	if tmplDesc, ok := getRequiredTmplMsg(templateProto); !ok {
		m.addError(templateProto.GetName(), unknownLine, "message 'Template' not defined")
	} else {
		m.addTemplateMessage(parser, templateProto, tmplDesc)
	}
}

func (m *Model) addTemplateMessage(parser *FileDescriptorSetParser, tmplProto *FileDescriptor, tmplDesc *Descriptor) {
	m.TemplateMessage.Comment = tmplProto.getComment(tmplDesc.path)
	m.TemplateMessage.Fields = make([]FieldInfo, 0)
	for i, fieldDesc := range tmplDesc.Field {
		fieldName := fieldDesc.GetName()

		// Name field is a reserved field that will be injected in the Instance object. The user defined
		// Template should not have a Name field, else there will be a name clash.
		// 'Name' within the Instance object would represent the name of the Instance:name
		// specified in the operator Yaml file.
		if strings.ToLower(fieldName) == "name" {
			m.addError(tmplDesc.file.GetName(),
				tmplProto.getLineNumber(getPathForField(tmplDesc, i)),
				"Template message must not contain the reserved filed name '%s'", fieldDesc.GetName())
			continue
		}

		protoTypeInfo, goTypeInfo, err := getTypeName(parser, fieldDesc)
		if err != nil {
			m.addError(tmplDesc.file.GetName(),
				tmplProto.getLineNumber(getPathForField(tmplDesc, i)),
				err.Error())
		}
		m.TemplateMessage.Fields = append(m.TemplateMessage.Fields, FieldInfo{
			ProtoName: fieldName,
			GoName:    camelCase(fieldName),
			GoType:    goTypeInfo,
			ProtoType: protoTypeInfo,
			Number:    strconv.Itoa(int(fieldDesc.GetNumber())),
			Comment:   tmplProto.getComment(getPathForField(tmplDesc, i)),
		})
	}
}

// Find the file that has the options TemplateVariety and TemplateName. There should only be one such file.
func getTmplFileDesc(fds []*FileDescriptor) (*FileDescriptor, []diag) {
	var templateDescriptorProto *FileDescriptor
	diags := make([]diag, 0)
	for _, fd := range fds {
		if fd.GetOptions() == nil {
			continue
		}
		if !proto.HasExtension(fd.GetOptions(), tmpl.E_TemplateName) && !proto.HasExtension(fd.GetOptions(), tmpl.E_TemplateVariety) {
			continue
		}

		if !proto.HasExtension(fd.GetOptions(), tmpl.E_TemplateName) || !proto.HasExtension(fd.GetOptions(), tmpl.E_TemplateVariety) {
			diags = append(diags, createError(fd.GetName(), unknownLine,
				"Contains only one of the following two options %s and %s. Both options are required.",
				[]interface{}{tmpl.E_TemplateVariety.Name, tmpl.E_TemplateName.Name}))
			continue
		}

		if templateDescriptorProto != nil {
			diags = append(diags, createError(fd.GetName(), unknownLine,
				"Proto files %s and %s, both have the options %s and %s. Only one proto file is allowed with those options",
				[]interface{}{fd.GetName(), templateDescriptorProto.Name, tmpl.E_TemplateVariety.Name, tmpl.E_TemplateName.Name}))
			continue
		}

		templateDescriptorProto = fd
	}

	if templateDescriptorProto == nil {
		diags = append(diags, createError(unknownFile, unknownLine, "There has to be one proto file that has both extensions %s and %s",
			[]interface{}{tmpl.E_TemplateVariety.Name, tmpl.E_TemplateVariety.Name}))
	}

	if len(diags) != 0 {
		return nil, diags
	}

	return templateDescriptorProto, nil
}

// Add all the file level fields to the model.
func (m *Model) addTopLevelFields(fd *FileDescriptor) {
	if !fd.proto3 {
		m.addError(fd.GetName(), fd.getLineNumber(syntaxPath), "Only proto3 template files are allowed")
	}

	if fd.Package != nil {
		m.PackageName = strings.TrimSpace(*fd.Package)
		m.GoPackageName = goPackageName(m.PackageName)
	} else {
		m.addError(fd.GetName(), unknownLine, "package name missing")
	}

	if tmplName, err := proto.GetExtension(fd.GetOptions(), tmpl.E_TemplateName); err == nil {
		if _, ok := tmplName.(*string); !ok {
			// protoc should mandate the type. It is impossible to get to this state.
			m.addError(fd.GetName(), unknownLine, "%s should be of type string", tmpl.E_TemplateName.Name)
		} else {
			m.Name = *tmplName.(*string)
			if err := validateTmplName(m.Name); err != nil {
				m.addError(fd.GetName(), unknownLine, err.Error())
			}
		}
	} else {
		// This func should only get called for FileDescriptor that has this attribute,
		// therefore it is impossible to get to this state.
		m.addError(fd.GetName(), unknownLine, "file option %s is required", tmpl.E_TemplateName.Name)
	}

	if tmplVariety, err := proto.GetExtension(fd.GetOptions(), tmpl.E_TemplateVariety); err == nil {
		m.VarietyName = (*(tmplVariety.(*tmpl.TemplateVariety))).String()
	} else {
		// This func should only get called for FileDescriptor that has this attribute,
		// therefore it is impossible to get to this state.
		m.addError(fd.GetName(), unknownLine, "file option %s is required", tmpl.E_TemplateVariety.Name)
	}

	// For file level comments, comments from multiple locations are composed.
	m.Comment = fmt.Sprintf("%s\n%s", fd.getComment(syntaxPath), fd.getComment(packagePath))
}

// Template name must contain only alphanumerics with underscore. First character must be a capital alphabet.
const tmplNamePattern = "^[A-Z][a-zA-Z0-9_]+$"

func validateTmplName(name string) error {
	if b, err := regexp.Match(tmplNamePattern, []byte(name)); err != nil {
		return fmt.Errorf("template name '%s' cannot be validated: %v", name, err)
	} else if !b {
		return fmt.Errorf("template name '%s' does not match the pattern %s", name, tmplNamePattern)
	}
	return nil
}

func getRequiredTmplMsg(fdp *FileDescriptor) (*Descriptor, bool) {
	var cstrDesc *Descriptor
	for _, desc := range fdp.desc {
		if desc.GetName() == "Template" {
			cstrDesc = desc
			break
		}
	}

	return cstrDesc, cstrDesc != nil
}

func createInvalidTypeError(field string, err error) error {
	errStr := fmt.Sprintf("unsupported type for field '%s'. Supported types are '%s'", field, supportedTypes)
	if err == nil {
		return fmt.Errorf(errStr)
	}
	return fmt.Errorf(errStr+": %v", err)

}
func getTypeName(g *FileDescriptorSetParser, field *descriptor.FieldDescriptorProto) (protoType TypeInfo, goType TypeInfo, err error) {
	switch *field.Type {
	case descriptor.FieldDescriptorProto_TYPE_STRING:
		return TypeInfo{Name: "string"}, TypeInfo{Name: sSTRING}, nil
	case descriptor.FieldDescriptorProto_TYPE_INT64:
		return TypeInfo{Name: "int64"}, TypeInfo{Name: sINT64}, nil
	case descriptor.FieldDescriptorProto_TYPE_DOUBLE:
		return TypeInfo{Name: "double"}, TypeInfo{Name: sFLOAT64}, nil
	case descriptor.FieldDescriptorProto_TYPE_BOOL:
		return TypeInfo{Name: "bool"}, TypeInfo{Name: sBOOL}, nil
	case descriptor.FieldDescriptorProto_TYPE_ENUM:
		if field.GetTypeName()[1:] == fullProtoNameOfValueTypeEnum {
			desc := g.ObjectNamed(field.GetTypeName())
			return TypeInfo{Name: field.GetTypeName()[1:], IsValueType: true}, TypeInfo{Name: g.TypeName(desc), IsValueType: true}, nil
		}
	case descriptor.FieldDescriptorProto_TYPE_MESSAGE:
		desc := g.ObjectNamed(field.GetTypeName())
		if d, ok := desc.(*Descriptor); ok && d.GetOptions().GetMapEntry() {
			keyField, valField := d.Field[0], d.Field[1]

			protoKeyType, goKeyType, err := getTypeName(g, keyField)
			if err != nil {
				return TypeInfo{}, TypeInfo{}, createInvalidTypeError(field.GetName(), err)
			}
			protoValType, goValType, err := getTypeName(g, valField)
			if err != nil {
				return TypeInfo{}, TypeInfo{}, createInvalidTypeError(field.GetName(), err)
			}

			if protoKeyType.Name == "string" {
				return TypeInfo{
						Name:     fmt.Sprintf("map<%s, %s>", protoKeyType.Name, protoValType.Name),
						IsMap:    true,
						MapKey:   &protoKeyType,
						MapValue: &protoValType,
					},
					TypeInfo{
						Name:     fmt.Sprintf("map[%s]%s", goKeyType.Name, goValType.Name),
						IsMap:    true,
						MapKey:   &goKeyType,
						MapValue: &goValType,
					},
					nil
			}
		}
	default:
		return TypeInfo{}, TypeInfo{}, createInvalidTypeError(field.GetName(), nil)
	}
	return TypeInfo{}, TypeInfo{}, createInvalidTypeError(field.GetName(), nil)
}
