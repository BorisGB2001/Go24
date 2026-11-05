package update

// PublicKey is the base64 ed25519 public key the releases of go24 are signed with (the private one is kept by the
// publisher only, tools publish.sh signs go24-X.Y.Z.tar.gz with it). Empty — this build installs nothing.
var PublicKey = ""
