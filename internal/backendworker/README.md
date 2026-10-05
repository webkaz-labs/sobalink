# Backend worker pipe protocol

This package is a bounded owner-pipe RPC foundation for process-isolated network engines. It does not launch an engine or enable mixed networking by itself.

Messages use a length-prefixed JSON envelope with a 256 KiB maximum and 128 outstanding requests. The owning application supplies a fixed typed handler and closes worker resources when the owner disappears. No shell execution or generic management command is provided. Cancellation terminates the affected worker generation rather than replaying data or retaining abandoned operations.

Application integration must launch the same verified executable with separate backend state, pipe ownership, reserved ports and generations. It must implement typed state, identity, stream and datagram operations with finite resource budgets. Backend authentication, resource grants and route boundaries remain independent checks. A worker crash can close active streams; no transparent TCP migration is claimed.

Socket-free pipe tests cover concurrent calls, owner EOF, cancellation and oversized-frame rejection. Native process/engine integration is still required.
