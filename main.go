package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path"
	"time"
)

const (
	superblockSize    = 96
	magicByte         = 0x73717368
	basicDirectory    = 1
	extendedDirectory = 8
	inodeHeaderSize   = 16
	maxDirEntries     = 256
	dirHeaderSize     = 12
	dirEntryMinSize   = 8
	dirNameMaxSize    = 256
	metadataSize      = 8192
)

type inodeType uint16

const (
	inodeBasicDirectory    inodeType = 1
	inodeBasicFile                   = 2
	inodeBasicSymlink                = 3
	inodeBasicBlock                  = 4
	inodeBasicChar                   = 5
	inodeBasicFifo                   = 6
	inodeBasicSocket                 = 7
	inodeExtendedDirectory           = 8
	inodeExtendedFile                = 9
	inodeExtendedSymlink             = 10
	inodeExtendedBlock               = 11
	inodeExtendedChar                = 12
	inodeExtendedFifo                = 13
	inodeExtendedSocket              = 14
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

type printableNumber struct {
	name string
	data uint64
}

func (p *printableNumber) print() string {
	return fmt.Sprintf("%-30s %#10x %10d", p.name, p.data, p.data)
}

type inodeHeader struct {
	inodeType inodeType
	uidIdx    uint16
	gidIdx    uint16
	modTime   time.Time
	index     uint32
	mode      os.FileMode
}

type inodePointer struct {
	path      string
	inodeType inodeType
	block     uint32
	offset    uint16
	dirBlock  uint32
	dirOffset uint16
	dirSize   uint16
}

func (i *inodePointer) print() string {
	in := fmt.Sprintf("%-30s: %4v %#10x %#10x , %10d %10d", i.path, i.inodeType, i.block, i.offset, i.block, i.offset)
	if i.dirSize > 0 {
		in = fmt.Sprintf("%s, %#10x %#10x , %10d %10d, %4d", in, i.dirBlock, i.dirOffset, i.dirBlock, i.dirOffset, i.dirSize)
	}
	return in
}
func (i *inodePointer) printHeader() string {
	return fmt.Sprintf("%-30s: %4s %20s, %20s, %20s, %20s, %4s", "path", "type", "Inode Hex Block/Offset", "Inode Decimal Block/Offset", "Dir Hex Block/Offset", "Dir Decimal Block/Offset", "Dir Size")
}

func readInodeHeader(f io.ReaderAt, offset int64) inodeHeader {
	b := make([]byte, inodeHeaderSize)
	n, err := f.ReadAt(b, offset)
	if err != nil {
		log.Fatalf("error reading inode header at %d: %v", offset, err)
	}
	if n != len(b) {
		log.Fatalf("read %d instead of expected %d inode header bytes at %d", n, len(b), offset)
	}
	return inodeHeader{
		inodeType: inodeType(binary.LittleEndian.Uint16(b[0:2])),
		mode:      os.FileMode(binary.LittleEndian.Uint16(b[2:4])),
		uidIdx:    binary.LittleEndian.Uint16(b[4:6]),
		gidIdx:    binary.LittleEndian.Uint16(b[6:8]),
		modTime:   time.Unix(int64(binary.LittleEndian.Uint32(b[8:12])), 0),
		index:     binary.LittleEndian.Uint32(b[12:16]),
	}
}

func parseDirectoryInode(b []byte, t inodeType) (uint32, uint16, uint16) {
	var (
		dirBlockIndex uint32
		dirSize       uint16
		offset        uint16
	)
	switch t {
	case basicDirectory:
		dirBlockIndex = binary.LittleEndian.Uint32(b[0:4])
		dirSize = binary.LittleEndian.Uint16(b[8:10])
		offset = binary.LittleEndian.Uint16(b[10:12])
	case extendedDirectory:
		dirBlockIndex = binary.LittleEndian.Uint32(b[8:12])
		dirSize = binary.LittleEndian.Uint16(b[4:8])
		offset = binary.LittleEndian.Uint16(b[18:20])
	}
	return dirBlockIndex, dirSize, offset
}

type directoryHeader struct {
	count      uint32
	startBlock uint32
	inode      uint32
}

type directoryEntryRaw struct {
	offset         uint16
	inodeNumber    uint16
	inodeType      inodeType
	name           string
	isSubdirectory bool
	startBlock     uint32
}

// parse the header of a directory
func parseDirectoryHeader(b []byte) (*directoryHeader, error) {
	if len(b) < dirHeaderSize {
		return nil, fmt.Errorf("Header was %d bytes, less than minimum %d", len(b), dirHeaderSize)
	}
	return &directoryHeader{
		count:      binary.LittleEndian.Uint32(b[0:4]) + 1,
		startBlock: binary.LittleEndian.Uint32(b[4:8]),
		inode:      binary.LittleEndian.Uint32(b[8:12]),
	}, nil
}

// parse a raw directory entry
func parseDirectoryEntry(b []byte) (*directoryEntryRaw, int, error) {
	// ensure we have enough bytes to parse
	if len(b) < dirEntryMinSize {
		return nil, 0, fmt.Errorf("Directory entry was %d bytes, less than minimum %d", len(b), dirEntryMinSize)
	}

	offset := binary.LittleEndian.Uint16(b[0:2])
	inode := binary.LittleEndian.Uint16(b[2:4])
	entryType := inodeType(binary.LittleEndian.Uint16(b[4:6]))
	nameSize := binary.LittleEndian.Uint16(b[6:8])
	realNameSize := nameSize + 1

	// make sure name is legitimate size
	if nameSize > dirNameMaxSize {
		return nil, 0, fmt.Errorf("Name size was %d bytes, greater than maximum %d", nameSize, dirNameMaxSize)
	}
	if int(realNameSize+dirEntryMinSize) > len(b) {
		return nil, 0, fmt.Errorf("Dir entry plus size of name is %d, larger than available bytes %d", nameSize+dirEntryMinSize, len(b))
	}

	// read in the name
	name := string(b[8 : 8+realNameSize])
	return &directoryEntryRaw{
		offset:      offset,
		inodeNumber: inode,
		name:        name,
		inodeType:   entryType,
	}, int(8 + realNameSize), nil
}

func parseDirectory(p string, b []byte) ([]*inodePointer, error) {
	var entries []*inodePointer
	for pos := 0; pos+dirHeaderSize < len(b); {
		directoryHeader, err := parseDirectoryHeader(b[pos:])
		if err != nil {
			return nil, fmt.Errorf("Could not parse directory header: %v", err)
		}
		if directoryHeader.count+1 > maxDirEntries {
			return nil, fmt.Errorf("Corrupted directory, had %d entries instead of max %d", directoryHeader.count+1, maxDirEntries)
		}
		pos += dirHeaderSize
		for count := uint32(0); count < directoryHeader.count; count++ {
			entry, size, err := parseDirectoryEntry(b[pos:])
			if err != nil {
				return nil, fmt.Errorf("Unable to parse entry at position %d: %v", pos, err)
			}
			entry.startBlock = directoryHeader.startBlock
			entries = append(entries, &inodePointer{
				path:      path.Join(p, entry.name),
				block:     entry.startBlock,
				offset:    entry.offset,
				inodeType: entry.inodeType,
			})
			// increment the position
			pos += size
		}
	}
	return entries, nil
}

func readMetadataBlock(r io.ReaderAt, location int64) (int, []byte, error) {
	// read the size and compression
	b := make([]byte, 2)
	n, err := r.ReadAt(b, location)
	if err != nil {
		return 0, nil, fmt.Errorf("could not read size bytes for metadata block at %d: %v", location, err)
	}
	if n != len(b) {
		return 0, nil, fmt.Errorf("read %d instead of expected %d bytes for metadata block at location %d", n, len(b), location)
	}
	header := binary.LittleEndian.Uint16(b[:2])
	size := header & 0x7fff
	compressed := header&0x8000 != 0x8000
	// we do not handle compressed yet
	if compressed {
		return 0, nil, fmt.Errorf("unable to read compressed metadata blocks yet at location %d", location)
	}
	b = make([]byte, size)
	n, err = r.ReadAt(b, location+2)
	if err != nil {
		return 0, nil, fmt.Errorf("could not data size bytes for metadata block at %d: %v", location, err)
	}
	if n != len(b) {
		return 0, nil, fmt.Errorf("read %d instead of expected %d bytes for metadata block at location %d", n, len(b), location)
	}
	return len(b) + 2, b, nil
}

// readMetadata read as many bytes of metadata as required for the given size, with the byteOffset provided as a starting
// point into the first block. Can read multiple blocks if necessary, e.g. if a block is 8192 bytes (standard), and
// requests to read 500 bytes beginning at offset 8000 into the first block.
func readMetadata(r io.ReaderAt, firstBlock int64, initialBlockOffset uint32, byteOffset uint16, size int) ([]byte, error) {
	var (
		b           []byte
		blockOffset = int(initialBlockOffset)
	)
	// we know how many blocks, so read them all in
	read, m, err := readMetadataBlock(r, firstBlock+int64(blockOffset))
	if err != nil {
		return nil, err
	}
	b = append(b, m[byteOffset:]...)
	// do we have any more to read?
	for len(b) < size {
		blockOffset += read
		read, m, err = readMetadataBlock(r, firstBlock+int64(blockOffset))
		if err != nil {
			return nil, err
		}
		b = append(b, m...)
	}
	if len(b) >= size {
		b = b[:size]
	}
	return b, nil
}

func walkTree(f *os.File, inode *inodePointer, inodeTable uint64, directoryTable uint64) []*inodePointer {
	ret := []*inodePointer{inode}
	start := inodeTable + uint64(inode.block*(metadataSize+2)) + 2 + uint64(inode.offset)
	header := readInodeHeader(f, int64(start))
	// if it is a directory, walk children
	start += inodeHeaderSize
	switch header.inodeType {
	case basicDirectory, extendedDirectory:
		b := make([]byte, 20)
		n, err := f.ReadAt(b, int64(start))
		if err != nil {
			log.Fatalf("error reading inode body at %d: %v", start, err)
		}
		if n != len(b) {
			log.Fatalf("read %d instead of expected %d bytes for body at %d", n, len(b), start)
		}
		dirBlockIndex, dirSize, offset := parseDirectoryInode(b, header.inodeType)
		inode.dirBlock = dirBlockIndex
		inode.dirOffset = offset
		inode.dirSize = dirSize
		// read the directory entries
		b, err = readMetadata(f, int64(directoryTable), dirBlockIndex, offset, int(dirSize))
		if err != nil {
			log.Fatalf("error reading directory at %d: %v", start, err)
		}
		// read the directory entries until done
		inodes, err := parseDirectory(inode.path, b)
		if err != nil {
			log.Fatalf("error parsing directory at %s: %v", inode.path, err)
		}
		for _, in := range inodes {
			ret = append(ret, walkTree(f, in, inodeTable, directoryTable)...)
		}
	}
	if inode.inodeType != header.inodeType {
		inode.inodeType = header.inodeType
	}
	return ret
}

func parseInodeRef(ref uint64) (uint32, uint16) {
	return uint32((ref >> 16) & 0xffffffff), uint16(ref & 0xffff)
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
	rootInodeBlock, rootInodeOffset := parseInodeRef(rootInodeRef)
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

	data := []printableNumber{
		{"root inode block", uint64(rootInodeBlock)},
		{"root inode offset", uint64(rootInodeOffset)},
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
		fmt.Println(d.print())
	}
	// now print the filesystem contents
	files := walkTree(f, &inodePointer{"/", inodeBasicDirectory, rootInodeBlock, rootInodeOffset, 0, 0, 0}, inodeTableStart, dirTableStart)
	fmt.Println()
	for i, d := range files {
		if i == 0 {
			fmt.Println(d.printHeader())
		}
		fmt.Println(d.print())
	}
}
