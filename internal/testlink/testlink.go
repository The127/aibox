// Package testlink makes the links tests check that aibox follows. A
// symlink takes a privilege on Windows that a user mostly lacks, so there
// it makes a junction instead, which aibox must follow as well.
package testlink
