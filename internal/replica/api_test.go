package replica

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestReplicaExportedAPISurface — эталон экспортированной поверхности пакета:
// view-типы инкапсулированы, новые экспорты — осознанное решение (машинный
// тест приватности, ADR-0004 ось 1 / D4).
func TestReplicaExportedAPISurface(t *testing.T) {
	t.Parallel()
	want := map[string]bool{
		// типы
		"CellID": true, "Record": true, "RecordKind": true, "RecordKindPlayer": true,
		"RecordKindNPC": true, "Blob": true, "Publisher": true, "Join": true,
		"JoinConfig": true, "Observer": true, "Event": true, "EventKind": true,
		"EventIntroduce": true, "EventRemove": true, "EventUpdate": true,
		"Snapshot": true, "MembershipHeader": true, "GroundItem": true,
		"Advisory": true, "AdvisoryInput": true, "Flags": true, "FlagHidden": true,
		// сетка ячеек (P4.1)
		"Grid": true, "NewGrid": true, "CellOf": true, "DefaultCellShift": true,
		// функции и методы
		"CanonJoinConfig": true, "Visible": true, "NewPublisher": true,
		"Build": true, "Commit": true, "Read": true, "Committed": true,
		"NewJoin": true, "Step": true, "Apply": true, "ForcePanicInApply": true,
		// sort.Interface плотной укладки (имена продиктованы stdlib)
		"Len": true, "Less": true, "Swap": true,
		// аксессоры Snapshot
		"Entity": true, "Pos": true, "Kind": true,
		// поля экспортированных типов-значений (Record/Event/Observer/
		// JoinConfig/MembershipHeader/GroundItem/Flags — «как значения»)
		"Cell": true, "X": true, "Y": true, "Z": true, "Heading": true,
		"DestX": true, "DestY": true, "DestZ": true,
		"Moving": true, "Name": true, "Race": true, "Female": true,
		"BaseClass": true, "ClassID": true, "HairStyle": true, "HairColor": true,
		"Face": true, "Enter": true, "Exit": true, "Obs": true, "Target": true,
		"ID": true, "TemplateID": true, "Count": true, "Generation": true, "ConnID": true,
		// поля NPC-записи (P3.10; примитивы — data-типов нет)
		"Title": true, "Attackable": true, "CollisionRadius": true, "CollisionHeight": true,
		"RunSpd": true, "WalkSpd": true, "SwimRunSpd": true, "SwimWalkSpd": true,
		"PAtkSpd": true, "MAtkSpd": true, "MoveMultiplier": true, "AttackSpeedMultiplier": true,
		"RHand": true, "LHand": true,
	}
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gen.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name.IsExported() {
						got[s.Name.Name] = true
					}
					if st, ok := s.Type.(*ast.StructType); ok {
						for _, fld := range st.Fields.List {
							for _, n := range fld.Names {
								if n.IsExported() {
									got[n.Name] = true
								}
							}
						}
					}
					if ifc, ok := s.Type.(*ast.InterfaceType); ok {
						for _, fld := range ifc.Methods.List {
							for _, n := range fld.Names {
								got[n.Name] = true
							}
						}
					}
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n.IsExported() {
							got[n.Name] = true
						}
					}
				}
			}
		}
		// функции и методы
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() {
				continue
			}
			got[fn.Name.Name] = true
		}
	}
	for name := range got {
		if !want[name] {
			t.Errorf("неожиданный экспорт %q вне эталона (утечка view-типа?)", name)
		}
	}
	for name := range want {
		if !got[name] {
			t.Errorf("эталонный экспорт %q отсутствует", name)
		}
	}
}
