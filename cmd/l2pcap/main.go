// l2pcap — конвертер пассивных захватов pcap/pcapng (dumpcap/Npcap) в бинарный
// журнал тапа. Оба контейнера читаются автоматически (классик и pcapng —
// Wireshark ≥3 пишет pcapng по умолчанию); TCP-потоки собираются в соединения
// (клиент = источник SYN; повторный SYN той же 4-тупы — новое соединение).
// Поток без SYN (захват с середины) пропускается с предупреждением; дыры seq
// перепрыгиваются при закрытии потока с счётом потерь; snaplen-обрезанные
// пакеты и IP-фрагменты пропускаются. Разбор журнала — l2tap -decode.
//
//	l2pcap -in capture.pcapng -out session.tap
//
// Журнал содержит данные сессии (шифротекст, чат, имена) — хранить как секрет.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/udisondev/l2go/internal/pcap"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			slog.Error("l2pcap: завершение с ошибкой", "err", err)
			os.Exit(1)
		}
		return
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("l2pcap", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "использование: l2pcap -in <захват pcap/pcapng> -out <журнал тапа>")
		fmt.Fprintln(os.Stderr, "  TCP-потоки собираются в соединения (клиент = источник SYN); разбор журнала — l2tap -decode.")
		fs.PrintDefaults()
	}
	in := fs.String("in", "", "файл захвата pcap или pcapng (обязателен)")
	out := fs.String("out", "", "файл журнала тапа (обязателен)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" || *out == "" {
		return errors.New("укажите -in <захват pcap/pcapng> и -out <журнал тапа> (-h — справка)")
	}
	raw, err := os.ReadFile(*in)
	if err != nil {
		return fmt.Errorf("чтение %s: %w", *in, err)
	}
	jf, err := os.Create(*out)
	if err != nil {
		return fmt.Errorf("журнал %s: %w", *out, err)
	}
	defer func() {
		if cerr := jf.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	rep, err := pcap.Convert(bytes.NewReader(raw), jf)
	slog.Info("l2pcap: конвертация",
		"conns", rep.Conns,
		"skippedNoSyn", rep.SkippedNoSyn,
		"nonTCP", rep.NonTCP,
		"snapped", rep.Snapped,
		"fragments", rep.Fragments,
		"resyncs", rep.Resyncs,
		"lostBytes", rep.LostBytes)
	if err != nil && !errors.Is(err, pcap.ErrTruncated) {
		return fmt.Errorf("%s: %w", *in, err)
	}
	if err != nil {
		// salvage: журнал написан целыми пакетами, хвост потерян — выходим
		// с ошибкой, чтобы потеря была видна
		return fmt.Errorf("%s: %w (журнал %s написан по целым пакетам)", *in, err, *out)
	}
	return nil
}
