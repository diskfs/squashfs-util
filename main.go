package main

import (
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"os"
	"time"
)

const (
	superblockSize = 96
	magicByte      = 0x73717368
)

type superblockFlags struct {
	uncompressedInodes    bool
	uncompressedData      bool
	uncompressedFragments bool
	noFragments           bool
	alwaysFragments       bool
	dedup                 bool
	exportable            bool
	uncompressedXattrs    bool
	noXattrs              bool
	compressorOptions     bool
	uncompressedIDs       bool
}

func parseFlags(flags uint16) *superblockFlags {
	s := &superblockFlags{
		uncompressedInodes:    flags&0x0001 == 0x0001,
		uncompressedData:      flags&0x0002 == 0x0002,
		uncompressedFragments: flags&0x0008 == 0x0008,
		noFragments:           flags&0x0010 == 0x0010,
		alwaysFragments:       flags&0x0020 == 0x0020,
		dedup:                 flags&0x0040 == 0x0040,
		exportable:            flags&0x0080 == 0x0080,
		uncompressedXattrs:    flags&0x0100 == 0x0100,
		noXattrs:              flags&0x0200 == 0x0200,
		compressorOptions:     flags&0x0400 == 0x0400,
		uncompressedIDs:       flags&0x0800 == 0x0800,
	}
	return s
}

type printable struct {
	name string
	data uint64
}

func (p *printable) print() string {
	return fmt.Sprintf("%-30s %#10x %10d\n", p.name, p.data, p.data)
}

func main() {
	args := os.Args[1:]
	if len(args) != 1 {
		log.Fatalf("Usage: %s <filename>", os.Args[0])
	}

	f, err := os.Open(args[0])
	if err != nil {
		log.Fatalf("Error opening file %s: %v", args[0], err)
	}
	defer f.Close()

	// read the superblock
	b := make([]byte, superblockSize)
	read, err := f.ReadAt(b, 0)
	if err != nil {
		log.Fatalf("Error reading superblock: %v", err)
	}
	if read != len(b) {
		log.Fatalf("Failed to read superblock, read %d bytes instead of expected %d", read, len(b))
	}
	// check magic bytes
	readMagic := binary.LittleEndian.Uint32(b[0:4])
	if readMagic != magicByte {
		log.Fatalf("Corrupt sqsh filesystem. Magic bytes were %x instead of %x", readMagic, magicByte)
	}
	// just read and parse out each piece
	inodeCount := binary.LittleEndian.Uint32(b[4:8])
	modTime := binary.LittleEndian.Uint32(b[8:12])
	blockSize := binary.LittleEndian.Uint32(b[12:16])
	fragCount := binary.LittleEndian.Uint32(b[16:20])
	compression := binary.LittleEndian.Uint16(b[20:22])
	blockLog := binary.LittleEndian.Uint16(b[22:24])

	expectedLog := uint16(math.Log2(float64(blockSize)))
	if expectedLog != blockLog {
		log.Fatalf("Corrupt sqsh filesystem. Log2 of blocksize was %d, expected %d", blockLog, expectedLog)
	}
	flags := binary.LittleEndian.Uint16(b[24:26])
	idCount := binary.LittleEndian.Uint16(b[26:28])
	major := binary.LittleEndian.Uint16(b[28:30])
	minor := binary.LittleEndian.Uint16(b[30:32])
	rootInodeRef := binary.LittleEndian.Uint64(b[32:40])
	rootInodeBlock := uint32((rootInodeRef >> 16) & 0xffffffff)
	rootInodeOffset := uint16(rootInodeRef & 0xffff)
	size := binary.LittleEndian.Uint64(b[40:48])
	idTableStart := binary.LittleEndian.Uint64(b[48:56])
	xattrTableStart := binary.LittleEndian.Uint64(b[56:64])
	inodeTableStart := binary.LittleEndian.Uint64(b[64:72])
	dirTableStart := binary.LittleEndian.Uint64(b[72:80])
	fragTableStart := binary.LittleEndian.Uint64(b[80:88])
	exportTableStart := binary.LittleEndian.Uint64(b[88:96])

	sFlags := parseFlags(flags)
	// print everything
	fmt.Printf("compression %d\n", compression)
	fmt.Printf("version %d.%d\n", major, minor)
	fmt.Printf("mod time %v\n", time.Unix(int64(modTime), 0))
	fmt.Printf("flags %#v\n", sFlags)
	fmt.Printf("root inode block.offset 0x%x.0x%x %d.%d\n", rootInodeBlock, rootInodeOffset, rootInodeBlock, rootInodeOffset)

	data := []printable{
		{"filesystem size", uint64(size)},
		{"inodes", uint64(inodeCount)},
		{"blocksize", uint64(blockSize)},
		{"fragment count", uint64(fragCount)},
		{"id count", uint64(idCount)},
		{"id table start", uint64(idTableStart)},
		{"xattr table start", uint64(xattrTableStart)},

		{"inode table start", uint64(inodeTableStart)},
		{"directory table start", uint64(dirTableStart)},
		{"fragment table start", uint64(fragTableStart)},
		{"export table start", uint64(exportTableStart)},
	}

	for _, d := range data {
		fmt.Print(d.print())
	}
}
