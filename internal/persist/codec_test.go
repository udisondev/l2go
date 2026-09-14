package persist

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func TestCodecRoundtrip(t *testing.T) {
	req := Request{
		Op: OpCreateChar, Corr: 42, Account: "acc",
		Name: "Vasya", Sex: 1, HairStyle: 2, HairColor: 1, Face: 1,
	}
	buf, err := EncodeRequest(req)
	if err != nil {
		t.Fatalf("EncodeRequest() error = %v", err)
	}
	got, err := DecodeRequest(buf)
	if err != nil {
		t.Fatalf("DecodeRequest() error = %v", err)
	}
	if !reflect.DeepEqual(got, req) {
		t.Errorf("DecodeRequest(EncodeRequest(%+v)) = %+v", req, got)
	}

	rep := Reply{
		Op: OpCreateChar, Corr: 42, OK: true,
		Record: &CharRecord{Account: "acc", Name: "Vasya", Level: 1},
	}
	buf, err = EncodeReply(rep)
	if err != nil {
		t.Fatalf("EncodeReply() error = %v", err)
	}
	gotRep, err := DecodeReply(buf)
	if err != nil {
		t.Fatalf("DecodeReply() error = %v", err)
	}
	if *gotRep.Record != *rep.Record || gotRep.Corr != rep.Corr || !gotRep.OK {
		t.Errorf("DecodeReply(EncodeReply()) = %+v; want %+v", gotRep, rep)
	}

	chars := []CharRecord{mkChar("acc", "Vasya", 0), mkChar("acc", "Petya", 1)}
	listRep := Reply{Op: OpCharList, Corr: 7, OK: true, Chars: chars}
	buf, _ = EncodeReply(listRep)
	gotList, err := DecodeReply(buf)
	if err != nil || len(gotList.Chars) != 2 || gotList.Chars[1] != chars[1] {
		t.Errorf("раундтрип CharList: (%+v, %v)", gotList.Chars, err)
	}
}

func TestCodecCorrEchoField(t *testing.T) {
	buf, _ := EncodeRequest(Request{Op: OpCharList, Corr: 12345, Account: "acc"})
	var raw map[string]any
	if err := json.Unmarshal(buf, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["corr"] != float64(12345) {
		t.Errorf("поле corr не сериализовано: %v", raw)
	}
}

func TestCodecEvilPayloads(t *testing.T) {
	for name, payload := range map[string]string{
		"пусто":      "",
		"обрезанный": `{"op":"charlist","corr":1,"acc`,
		"не объект":  `[1,2,3]`,
		"чужой тип":  `{"op":42}`,
		"null":       `null`,
	} {
		if _, err := DecodeRequest([]byte(payload)); err == nil {
			t.Errorf("DecodeRequest(%s) = nil; want ошибка", name)
		}
		if _, err := DecodeReply([]byte(payload)); err == nil {
			t.Errorf("DecodeReply(%s) = nil; want ошибка", name)
		}
	}
	if _, err := DecodeRequest([]byte("\x00\x01\x02")); err == nil {
		t.Error("DecodeRequest(бинарный мусор) = nil; want ошибка")
	}
}

func ExampleEncodeRequest() {
	buf, err := EncodeRequest(Request{Op: OpCharList, Corr: 7, Account: "player1"})
	if err != nil {
		fmt.Println("ошибка:", err)
		return
	}
	req, err := DecodeRequest(buf)
	if err != nil {
		fmt.Println("ошибка:", err)
		return
	}
	fmt.Println(req.Op, req.Corr, req.Account)
	// Output: charlist 7 player1
}
