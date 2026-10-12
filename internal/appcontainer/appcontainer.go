// Package appcontainer starts a program in an AppContainer of Windows, the
// sandbox that gives a process no access to the files of the user and,
// without a capability that grants it, none to the network. The program
// gets only the handles it is given, an environment it is given, and is
// killed with the job it runs in, so that it ends with whoever started
// it. It knows nothing of aibox.
package appcontainer
