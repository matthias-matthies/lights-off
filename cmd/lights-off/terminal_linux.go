package main

// getTermios is TCGETS, which the syscall package doesn't define. It is the same
// on amd64 and arm64, the architectures sbx supports on Ubuntu.
const getTermios = 0x5401
