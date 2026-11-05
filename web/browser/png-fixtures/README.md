# Fictional public-card PNG test images

Twelve representative static PNGs (140,593 bytes total) provide test inputs, not verification results. Every key, alias and address is fictional. The [manifest](manifest.json) records each PNG’s SHA-256 checksum and exact expected encoded card text. Coverage: both modes; minimum and maximally escaped aliases; Japanese aliases; optional address hints that do not change settings; forced QR versions 22/23/24; 256/512/1024/2048 pixel examples; rotation and transparency. These sample dimensions are not product maxima.

The larger independent set of 227 Node test cases is retained separately. Its 227/227 result is not a product-browser result. The browser spec reruns only these twelve through the shipped worker; screenshot, malformed/ambiguity, cancellation and CSP tests are separate. No RGBA buffers or runtime logs are retained here.

## Use and interpret these fixtures

1. Check each selected image against its manifest checksum and expected encoded text.
2. Run the shipped-reader cases through [browser acceptance](../../BROWSER_ACCEPTANCE.md).
3. Record the actual browser result separately from the independent Node test set.

A decoded card only fills reviewable input. It does not establish trust, pairing, permission or a network connection. Keep these fixtures synthetic when extending coverage.
