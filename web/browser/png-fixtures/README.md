# Synthetic public-card PNG browser fixtures

Twelve representative static PNGs (140,593 bytes total). Every key, alias and address is synthetic. The manifest binds each PNG digest and exact expected ASCII wire text. Coverage: both modes; minimum and maximally escaped aliases; Japanese aliases; optional inert hints; forced QR versions 22/23/24; 256/512/1024/2048 pixel examples; rotation and transparency. These sample dimensions are not product maxima.

The larger 227-case independent Node corpus is retained separately. Its 227/227 result is not a product-browser result. The browser spec reruns only these twelve through the shipped worker; screenshot, malformed/ambiguity, cancellation and CSP tests are separate. No RGBA buffers or runtime logs are retained here.

## Use and interpret these fixtures

1. Check each selected image against its manifest digest and expected wire text.
2. Run the shipped-reader cases through [browser acceptance](../../BROWSER_ACCEPTANCE.md).
3. Record the actual browser result separately from the independent Node corpus.

A decoded card only fills reviewable input. It does not establish trust, pairing, permission or a network connection. Keep these fixtures synthetic when extending coverage.
