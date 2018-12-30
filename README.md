# squashfs-util

Simple utility to read information about a squashfs filesystem.

As of now, just dumps the superblock information. If you want a full-blown ability to manipulate these systems, see [diskfs](https://github.com/diskfs) and its various libraries. This originally was developed to help work on squashfs for [diskfs](https://github.com/diskfs).

## Running
```
squashfs-util <filename>
```

## Building
```
make build
```

Will install in the same directory `squashfs-util` .

## Installing
```
make install
```

Will install in `$GOPATH/bin/`
