// l2ini — кодек l2.ini формата 413 (Lineage2Ver413): расшифровка, шифрование
// и проверка CRC-хвоста конфигурации клиента L2.
//
//	Расшифровка (modern-семейство — патченные клиенты):
//	  l2ini -decode l2.ini -out plain.txt
//	Расшифровка стокового клиентского файла:
//	  l2ini -decode l2.ini -legacy -out plain.txt
//	Шифрование (всегда modern; legacy не имеет encrypt-экспоненты):
//	  l2ini -encode plain.txt -out l2.ini
//	Проверка CRC-хвоста:
//	  l2ini -verify l2.ini
//
// Ключи l2encdec — публичные константы; живые l2.ini — файлы клиента NCsoft,
// в репозиторий не попадают.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/udisondev/l2go/internal/l2ini"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			slog.Error("l2ini: завершение с ошибкой", "err", err)
			os.Exit(1)
		}
		return
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("l2ini", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "использование: l2ini -decode <l2.ini> [-legacy] [-out <файл>] | -encode <текст> -out <l2.ini> |")
		fmt.Fprintln(os.Stderr, "           l2ini -verify <l2.ini>")
		fs.PrintDefaults()
	}
	decode := fs.String("decode", "", "файл l2.ini для расшифровки")
	encode := fs.String("encode", "", "файл открытого текста для шифрования (modern-ключ)")
	verify := fs.String("verify", "", "файл l2.ini для проверки CRC-хвоста")
	out := fs.String("out", "", "файл результата (по умолчанию stdout)")
	legacy := fs.Bool("legacy", false, "расшифровывать семейством legacy 413 (стоковые клиентские файлы)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	modes := 0
	for _, m := range []*string{decode, encode, verify} {
		if *m != "" {
			modes++
		}
	}
	if modes != 1 {
		return errors.New("укажите ровно один режим: -decode, -encode или -verify (-h — справка)")
	}

	switch {
	case *decode != "":
		data, err := os.ReadFile(*decode)
		if err != nil {
			return fmt.Errorf("чтение %s: %w", *decode, err)
		}
		key := l2ini.Modern
		if *legacy {
			key = l2ini.Legacy413
		}
		plain, err := l2ini.Decode(data, key)
		if err != nil {
			return fmt.Errorf("%s: %w", *decode, err)
		}
		return writeOut(*out, plain)
	case *encode != "":
		plain, err := os.ReadFile(*encode)
		if err != nil {
			return fmt.Errorf("чтение %s: %w", *encode, err)
		}
		file, err := l2ini.Encode(plain, l2ini.Modern)
		if err != nil {
			return fmt.Errorf("%s: %w", *encode, err)
		}
		return writeOut(*out, file)
	default:
		data, err := os.ReadFile(*verify)
		if err != nil {
			return fmt.Errorf("чтение %s: %w", *verify, err)
		}
		if err := l2ini.Verify(data); err != nil {
			return fmt.Errorf("%s: %w", *verify, err)
		}
		slog.Info("l2ini: CRC-хвост сходится", "file", *verify, "bytes", len(data))
		return nil
	}
}

func writeOut(path string, data []byte) error {
	if path == "" {
		_, err := io.Copy(os.Stdout, bytes.NewReader(data))
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
