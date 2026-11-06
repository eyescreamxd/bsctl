DeepFilterNet3 VST2 (x64) for Equalizer APO: the Alt Denoiser plugin

alt_denoiser.dll  = "VST/Alt Denoiser.dll" from Altinus/Alt-Denoiser v1.0.3
                    https://github.com/Altinus/Alt-Denoiser/releases/tag/v1.0.3
                    archive Alt-Denoiser-windows.zip, sha256 4343523e23e02df89086f99f0dd3459e2ec2ea7d675a37943c91163a12d49a16
                    dll sha256 3c0f83b26d69edead94dd155f5830a03c8658570a703f0eb42c8ee6a703de096
LICENSE.txt       = GPL-3.0: Alt Denoiser and JUCE (the upstream README states GPLv3; the repository has no
                    license file, so the standard GPL-3.0 text is put here). Source: the repository above.
LICENSE-DeepFilterNet-MIT.txt, LICENSE-DeepFilterNet-APACHE.txt
                  = DeepFilterNet (Hendrik Schroeter), MIT or Apache-2.0; the model is embedded in the DLL.

Unmodified binary. Needs the Microsoft Visual C++ 2015-2022 x64 runtime (msvcp140.dll, vcruntime140_1.dll).
BlackSharkTray copies it to <Equalizer APO>\config\bsctl\ when DeepFilterNet is selected.
