// ggctl is a command-line companion to ggd for inspecting devices and
// exercising device specifications directly.
//
//	ggctl hid                     list SteelSeries hidraw nodes
//	ggctl devices                 connected devices with a specification
//	ggctl apis PID                apis a device declares
//	ggctl read PID API            read an api and print the decoded reply
//	ggctl call PID FUNCTION       call a function from the device spec
//	ggctl write PID API JSON      write an api (changes device state!)
//	ggctl moments clip            save a Moments clip (bind it to a key)
//	ggctl moments toggle|mic|desktop  switch capture, mic, desktop capture
//	ggctl moments status          what Moments is recording
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/steelseries/golisp"

	"ggl/internal/devicert"
	"ggl/internal/engine"
	"ggl/internal/hid"
	"ggl/internal/paths"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	level := slog.LevelWarn
	if os.Getenv("GG_LINUX_DEBUG") != "" {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	if err := run(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "ggctl:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: ggctl hid | devices | apis PID | read PID API | call PID FUNCTION | write PID API JSON | moments clip|status")
	os.Exit(2)
}

func parsePID(s string) (uint32, error) {
	n, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(s), "0x"), 16, 32)
	if err != nil {
		return 0, err
	}
	if n <= 0xffff {
		n |= hid.SteelSeriesVID << 16
	}
	return uint32(n), nil
}

func run(cmd string, args []string) error {
	if cmd == "moments" {
		return momentsCommand(args)
	}
	if cmd == "hid" {
		infos, err := hid.Enumerate()
		if err != nil {
			return err
		}
		sort.Slice(infos, func(i, j int) bool { return infos[i].Path < infos[j].Path })
		for _, in := range infos {
			fmt.Printf("%-14s %04x:%04x if%d numbered=%v  %s\n", in.Path, in.VendorID, in.ProductID, in.Interface, in.Desc.Numbered, in.Name)
			for _, c := range in.Desc.Collections {
				fmt.Printf("    collection %04x:%04x ids=%v\n", c.UsagePage, c.Usage, c.ReportIDs)
			}
		}
		return nil
	}

	mgr := hid.NewManager(nil)
	mgr.Scan()
	defer mgr.Close()
	rt, err := devicert.New(paths.DeviceSpecs(), paths.EngineRoot(), engine.NewTransport(mgr), nil)
	if err != nil {
		return err
	}

	switch cmd {
	case "devices":
		for pid := range mgr.Products() {
			id := uint32(hid.SteelSeriesVID)<<16 | uint32(pid)
			if !rt.KnownDevice(id) {
				fmt.Printf("%#x  (no specification)\n", id)
				continue
			}
			d, err := rt.Device(id)
			if err != nil {
				fmt.Printf("%#x  %v\n", id, err)
				continue
			}
			fmt.Printf("%#x  %-40s usage %04x:%04x  %d apis\n", id, d.Name, d.UsagePage, d.Usage, len(d.ApiNames()))
		}
	case "apis":
		d, err := device(rt, args, 1)
		if err != nil {
			return err
		}
		names := d.ApiNames()
		sort.Strings(names)
		for _, n := range names {
			a := d.Api(n)
			rw := ""
			if a.Read != nil {
				rw += "r"
			}
			if len(a.Write) > 0 {
				rw += "w"
			}
			if a.IsFirmwareUpdate {
				rw += " firmware"
			}
			fmt.Printf("%-50s %s\n", n, rw)
		}
	case "read":
		d, err := device(rt, args, 2)
		if err != nil {
			return err
		}
		v, err := d.ReadApi(args[1], nil)
		if err != nil {
			return err
		}
		return printJSON(v)
	case "call":
		d, err := device(rt, args, 2)
		if err != nil {
			return err
		}
		v, err := d.Call(args[1])
		if err != nil {
			return err
		}
		fmt.Println(golisp.String(v))
	case "write":
		d, err := device(rt, args, 3)
		if err != nil {
			return err
		}
		var v any
		if err := json.Unmarshal([]byte(args[2]), &v); err != nil {
			return err
		}
		return d.WriteApi(args[1], v)
	default:
		usage()
	}
	return nil
}

func device(rt *devicert.Runtime, args []string, n int) (*devicert.Decl, error) {
	if len(args) < n {
		usage()
	}
	id, err := parsePID(args[0])
	if err != nil {
		return nil, err
	}
	return rt.Device(id)
}

func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}
