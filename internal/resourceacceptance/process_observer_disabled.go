//go:build !resource_process_native

package resourceacceptance

// Ordinary and N1/N4 builds retain no process binding, registration or I/O.
func ProcessOwnerLock(any)                                      {}
func ProcessWebOpened(any, string)                              {}
func ProcessIPCReady(any)                                       {}
func ProcessOwnersClosed(any, bool, bool)                       {}
func ProcessNodeConstructorAttempt(any, ProcessNodeConstructor) {}
func ProcessControlNodeConstructed(any, any)                    {}
func ProcessControlNodeJoined(any, any, bool)                   {}

func ProcessLocalCommand(any, string, string)                {}
func ProcessLocalLiteral(any, string)                        {}
func ProcessIPCConnectAttempt(string)                        {}
func ProcessIPCConnectCompleted(string)                      {}
func ProcessIPCRequestDispatched(string, ProcessCommandKind) {}
func ProcessIPCDirectoryMatches(string) bool                 { return false }
