"""One fixed hosted Linux P1 diagnostic. No effect occurs on import.

Publishing its exact reviewed tree and creating the one approved branch is the
external execution gate. Neither this source nor a self-measured hash grants it.
Owned subprocess logs stay private. GitHub runner/action logs are separate.
Sampled filesystem bounds are not OS quotas, egress isolation, or a guarantee
against blocking kernel calls. Missing or late evidence never means success.
"""
import datetime
import errno
import hashlib
import json
import os
import pathlib
import re
import signal
import stat
import struct
import subprocess
import sys
import threading
import time

ROOT = '/home/runner/work/sobalink/sobalink'

OUT = '/tmp/sbp1-ci'

BASE = '/tmp/sbp1-run'

GOROOT = '/opt/hostedtoolcache/go/1.27.1/x64'

PUBLIC_PARENT = 'e461c371b1ca6c1d1507db0e72261fe0c4695070'

PUBLIC_PARENT_TREE = '2729009e6699f7525fea3b3a4cb57f2d8edbd673'

REPOSITORY = 'webkaz-labs/sobalink'

BRANCH = 'diagnostic-p1/process-restart-86bbb925-once'

BASELINE_COMMIT = '86bbb92571dd09598f870b7256e5eb68bcd8bc6f'

BASELINE_TREE = '52d65ac279d8a20623fe248e21f698c36a82538a'

ANCHOR_SHA256 = 'e8f49555a35e4324bfaa8dec287f411cb02ed8a8deac0892e73f86bbb3c78858'

DEPENDENCY_SHA256 = 'a8095cbb138d55a870fb3658a182eb24986a2910520280bce5b586300b377f67'

TOOLCHAIN_SHA256 = '1181f01598b82bee282a40d234ba64a79f087723e6ac7960961c76b0d6d1ef89'

ACTIVE_SHA256 = '6b78c4a973291711bb3674f75b67095f00c22780d126dc4f488449cf553e5004'

OWNED_FILES = ['.github/fixtures/resource-process-restart/reviewed-inputs.json',
 '.github/scripts/ci-resource-process-restart.py',
 '.github/scripts/test_ci_resource_process_restart.py',
 '.github/workflows/resource-process-restart-diagnostic.yml']

ACTIONS = {'checkout': 'actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1',
 'uploadArtifact': 'actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a'}

OFFLINE_ENV = {'ALL_PROXY': '',
 'CGO_ENABLED': '0',
 'GIT_CONFIG_GLOBAL': '/dev/null',
 'GIT_CONFIG_NOSYSTEM': '1',
 'GIT_OPTIONAL_LOCKS': '0',
 'GOAMD64': 'v1',
 'GOARCH': 'amd64',
 'GOAUTH': 'off',
 'GOCACHE': '/tmp/sbp1-ci/gocache',
 'GOCACHEPROG': '',
 'GODEBUG': '',
 'GOENV': 'off',
 'GOEXPERIMENT': '',
 'GOFIPS140': 'off',
 'GOFLAGS': '-mod=readonly',
 'GOMAXPROCS': '2',
 'GOMODCACHE': '/tmp/sbp1-ci/gomodcache',
 'GONOPROXY': '',
 'GONOSUMDB': '',
 'GOOS': 'linux',
 'GOPATH': '/tmp/sbp1-ci/gopath',
 'GOPRIVATE': '',
 'GOPROXY': 'off',
 'GOROOT': '/opt/hostedtoolcache/go/1.27.1/x64',
 'GOSUMDB': 'off',
 'GOTELEMETRY': 'off',
 'GOTMPDIR': '/tmp/sbp1-ci/tmp',
 'GOTOOLCHAIN': 'local',
 'GOVCS': '*:off',
 'GOWORK': 'off',
 'HOME': '/tmp/sbp1-ci/home',
 'HTTPS_PROXY': '',
 'HTTP_PROXY': '',
 'LANG': 'C',
 'LC_ALL': 'C',
 'PATH': '/opt/hostedtoolcache/go/1.27.1/x64/bin:/usr/bin:/bin',
 'SOBALINK_RESOURCE_PROCESS_MANIFEST': '',
 'SOBALINK_RUN_ACTIVATION_NATIVE': '',
 'SOBALINK_RUN_MANAGED_RESTART_NATIVE': '',
 'SOBALINK_RUN_PRODUCT_ACTIVATION_NATIVE': '',
 'SOBALINK_RUN_RESOURCE_GROUP_CATALOG_NATIVE': '',
 'SOBALINK_RUN_RESOURCE_GROUP_RESTART_NATIVE': '',
 'SOBALINK_RUN_RESOURCE_INSPECTION_NATIVE': '',
 'SOBALINK_RUN_RESOURCE_MANAGEMENT_NATIVE': '',
 'SOBALINK_RUN_RESOURCE_PROCESS_NATIVE': '',
 'SOBALINK_RUN_WEB_ACTIVATION_NATIVE': '',
 'TMPDIR': '/tmp/sbp1-ci/tmp',
 'TS_PROXY': '',
 'TZ': 'UTC',
 'all_proxy': '',
 'http_proxy': '',
 'https_proxy': ''}

SETUP_ENV = {'ALL_PROXY': '',
 'CGO_ENABLED': '0',
 'GIT_CONFIG_GLOBAL': '/dev/null',
 'GIT_CONFIG_NOSYSTEM': '1',
 'GIT_OPTIONAL_LOCKS': '0',
 'GOAMD64': 'v1',
 'GOARCH': 'amd64',
 'GOAUTH': 'off',
 'GOCACHE': '/tmp/sbp1-ci/gocache',
 'GOCACHEPROG': '',
 'GODEBUG': '',
 'GOENV': 'off',
 'GOEXPERIMENT': '',
 'GOFIPS140': 'off',
 'GOFLAGS': '-mod=readonly',
 'GOMAXPROCS': '2',
 'GOMODCACHE': '/tmp/sbp1-ci/gomodcache',
 'GONOPROXY': '',
 'GONOSUMDB': '',
 'GOOS': 'linux',
 'GOPATH': '/tmp/sbp1-ci/gopath',
 'GOPRIVATE': '',
 'GOPROXY': 'https://proxy.golang.org',
 'GOROOT': '/opt/hostedtoolcache/go/1.27.1/x64',
 'GOSUMDB': 'sum.golang.org',
 'GOTELEMETRY': 'off',
 'GOTMPDIR': '/tmp/sbp1-ci/tmp',
 'GOTOOLCHAIN': 'local',
 'GOVCS': '*:off',
 'GOWORK': 'off',
 'HOME': '/tmp/sbp1-ci/home',
 'HTTPS_PROXY': '',
 'HTTP_PROXY': '',
 'LANG': 'C',
 'LC_ALL': 'C',
 'PATH': '/opt/hostedtoolcache/go/1.27.1/x64/bin:/usr/bin:/bin',
 'SOBALINK_RESOURCE_PROCESS_MANIFEST': '',
 'SOBALINK_RUN_ACTIVATION_NATIVE': '',
 'SOBALINK_RUN_MANAGED_RESTART_NATIVE': '',
 'SOBALINK_RUN_PRODUCT_ACTIVATION_NATIVE': '',
 'SOBALINK_RUN_RESOURCE_GROUP_CATALOG_NATIVE': '',
 'SOBALINK_RUN_RESOURCE_GROUP_RESTART_NATIVE': '',
 'SOBALINK_RUN_RESOURCE_INSPECTION_NATIVE': '',
 'SOBALINK_RUN_RESOURCE_MANAGEMENT_NATIVE': '',
 'SOBALINK_RUN_RESOURCE_PROCESS_NATIVE': '',
 'SOBALINK_RUN_WEB_ACTIVATION_NATIVE': '',
 'TMPDIR': '/tmp/sbp1-ci/tmp',
 'TS_PROXY': '',
 'TZ': 'UTC',
 'all_proxy': '',
 'http_proxy': '',
 'https_proxy': ''}

NATIVE_ENV = {'ALL_PROXY': '',
 'CGO_ENABLED': '0',
 'GIT_CONFIG_GLOBAL': '/dev/null',
 'GIT_CONFIG_NOSYSTEM': '1',
 'GIT_OPTIONAL_LOCKS': '0',
 'GOAMD64': 'v1',
 'GOARCH': 'amd64',
 'GOAUTH': 'off',
 'GOCACHE': '/tmp/sbp1-ci/gocache',
 'GOCACHEPROG': '',
 'GODEBUG': '',
 'GOENV': 'off',
 'GOEXPERIMENT': '',
 'GOFIPS140': 'off',
 'GOFLAGS': '-mod=readonly',
 'GOMAXPROCS': '2',
 'GOMODCACHE': '/tmp/sbp1-ci/gomodcache',
 'GONOPROXY': '',
 'GONOSUMDB': '',
 'GOOS': 'linux',
 'GOPATH': '/tmp/sbp1-ci/gopath',
 'GOPRIVATE': '',
 'GOPROXY': 'off',
 'GOROOT': '/opt/hostedtoolcache/go/1.27.1/x64',
 'GOSUMDB': 'off',
 'GOTELEMETRY': 'off',
 'GOTMPDIR': '/tmp/sbp1-run/tmp',
 'GOTOOLCHAIN': 'local',
 'GOVCS': '*:off',
 'GOWORK': 'off',
 'HOME': '/tmp/sbp1-run/home',
 'HTTPS_PROXY': '',
 'HTTP_PROXY': '',
 'LANG': 'C',
 'LC_ALL': 'C',
 'PATH': '/opt/hostedtoolcache/go/1.27.1/x64/bin:/usr/bin:/bin',
 'SOBALINK_RESOURCE_PROCESS_MANIFEST': '/tmp/sbp1-ci/native-manifest.json',
 'SOBALINK_RUN_ACTIVATION_NATIVE': '1',
 'SOBALINK_RUN_MANAGED_RESTART_NATIVE': '',
 'SOBALINK_RUN_PRODUCT_ACTIVATION_NATIVE': '',
 'SOBALINK_RUN_RESOURCE_GROUP_CATALOG_NATIVE': '',
 'SOBALINK_RUN_RESOURCE_GROUP_RESTART_NATIVE': '',
 'SOBALINK_RUN_RESOURCE_INSPECTION_NATIVE': '',
 'SOBALINK_RUN_RESOURCE_MANAGEMENT_NATIVE': '',
 'SOBALINK_RUN_RESOURCE_PROCESS_NATIVE': 'reviewed-three-process-restart-v1',
 'SOBALINK_RUN_WEB_ACTIVATION_NATIVE': '',
 'TMPDIR': '/tmp/sbp1-run/tmp',
 'TS_PROXY': '',
 'TZ': 'UTC',
 'all_proxy': '',
 'http_proxy': '',
 'https_proxy': ''}

LEAF_COMMANDS = [{'argv': ['/tmp/sbp1-ci/bin/resourceacceptance.test',
           '-test.run=^(TestProcessEntryArgumentsClosedValues|TestProcessEntryBuildInfoClosedValues|TestProcessEntryEnvironmentExactValues|TestProcessEntryRoleAndBindingValues|TestProcessEntrySignalCauseValues|TestProcessIPCDialAndDispatchShapes|TestProcessIPCRejectsWrongScopeAndCommands|TestProcessIPCScopeBoundAndNoInstall|TestProcessLocalCommandMappingAndDigest|TestProcessLocalLateAndConcurrentCalls|TestProcessLocalLiteralAndBadEntry|TestProcessObservationActualBindingAndLifecycle|TestProcessObservationConcurrentAdmissionAndImmutableAlias|TestProcessObservationConstructorLedger|TestProcessObservationControlJoinIsIdempotentAndFailureSticky|TestProcessObservationGlobalHooksRemainUninstalled|TestProcessObservationModeAndResourceShapes|TestProcessObservationRejectsUnboundAndRetiredAliases|TestProcessObservationWebIPCAndClosure|TestProcessOutputClosedValues|TestProcessOutputCopiesAndDrainsValues|TestProcessOutputModeBoundsValues|TestProcessPipeGuardsValues|TestProcessReporterEOFPhaseValues|TestProcessReporterFinalizationDeadlineValues|TestProcessReporterInvalidConstructorValues|TestProcessReporterLateFinalizationCannotRecoverValues|TestProcessReporterObserverFinalizationValues|TestProcessReporterOwnerClosureRequiredValues|TestProcessReporterPreparedFinalizationInvalidTransitionsValues|TestProcessReporterPreparedFinalizationNoRenewalValues|TestProcessReporterPreparedFinalizationValues)$',
           '-test.v=true',
           '-test.count=1',
           '-test.parallel=1',
           '-test.shuffle=off',
           '-test.timeout=2m'],
  'id': 'observer32',
  'package': 'github.com/webkaz-labs/sobalink/internal/resourceacceptance',
  'tests': ['TestProcessEntryArgumentsClosedValues',
            'TestProcessEntryBuildInfoClosedValues',
            'TestProcessEntryEnvironmentExactValues',
            'TestProcessEntryRoleAndBindingValues',
            'TestProcessEntrySignalCauseValues',
            'TestProcessIPCDialAndDispatchShapes',
            'TestProcessIPCRejectsWrongScopeAndCommands',
            'TestProcessIPCScopeBoundAndNoInstall',
            'TestProcessLocalCommandMappingAndDigest',
            'TestProcessLocalLateAndConcurrentCalls',
            'TestProcessLocalLiteralAndBadEntry',
            'TestProcessObservationActualBindingAndLifecycle',
            'TestProcessObservationConcurrentAdmissionAndImmutableAlias',
            'TestProcessObservationConstructorLedger',
            'TestProcessObservationControlJoinIsIdempotentAndFailureSticky',
            'TestProcessObservationGlobalHooksRemainUninstalled',
            'TestProcessObservationModeAndResourceShapes',
            'TestProcessObservationRejectsUnboundAndRetiredAliases',
            'TestProcessObservationWebIPCAndClosure',
            'TestProcessOutputClosedValues',
            'TestProcessOutputCopiesAndDrainsValues',
            'TestProcessOutputModeBoundsValues',
            'TestProcessPipeGuardsValues',
            'TestProcessReporterEOFPhaseValues',
            'TestProcessReporterFinalizationDeadlineValues',
            'TestProcessReporterInvalidConstructorValues',
            'TestProcessReporterLateFinalizationCannotRecoverValues',
            'TestProcessReporterObserverFinalizationValues',
            'TestProcessReporterOwnerClosureRequiredValues',
            'TestProcessReporterPreparedFinalizationInvalidTransitionsValues',
            'TestProcessReporterPreparedFinalizationNoRenewalValues',
            'TestProcessReporterPreparedFinalizationValues'],
  'timeoutSeconds': 130},
 {'argv': ['/tmp/sbp1-ci/bin/processmodel.test',
           '-test.run=^(TestBootstrapCodecBindingDigest|TestBootstrapCodecGoldenAndRoundTrip|TestBootstrapCodecMaximumAndCopies|TestBootstrapCodecMessageLength|TestBootstrapCodecRejectsMalformedInput|TestCanonicalIdentifiers|TestCheckpointCodecDecoderCopiesAndInvalidConstruction|TestCheckpointCodecGoldenAndCopies|TestCheckpointCodecHeaderAndMalformedInput|TestCheckpointCodecInvalidValuesAndNonceDigest|TestCheckpointCodecPlanBoundsAndUnusedRows|TestCheckpointCodecRequestSequenceLimits|TestCheckpointCodecStickyRejectionAndNoPartialAdvance|TestEventCodecRejectsMalformed|TestEventCodecRoundTrip|TestFrameCodecRejectsMalformed|TestFrameCodecRoundTrip|TestFrameMessageLengthClosedHeader|TestLaunchValuesClosedBindings|TestLaunchValuesPathLexicalBounds|TestLaunchValuesScheduleAndArgv|TestLaunchValuesWindowsDeviceNames|TestLifecycleCodecClosedShapes|TestLifecycleEventsShareCategoryAndStreamBounds|TestRecorderBoundsAndInvalid|TestRecorderCloseAndLate|TestRecorderConcurrentWriters|TestRecorderGapRemainsFailed|TestRecorderPrefixAndCopies|TestTranscriptBootBindingAndDigest|TestTranscriptDecoderCheckpointAndEntryOrdering|TestTranscriptDecoderCompleteCopies|TestTranscriptDecoderCumulativeEventBounds|TestTranscriptDecoderOutputAndCounterBounds|TestTranscriptDecoderStickyEvidenceFailure|TestTranscriptDecoderTerminalAndMalformed|TestTranscriptPayloadCodecCanonical)$',
           '-test.v=true',
           '-test.count=1',
           '-test.parallel=1',
           '-test.shuffle=off',
           '-test.timeout=2m'],
  'id': 'model37',
  'package': 'github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel',
  'tests': ['TestBootstrapCodecBindingDigest',
            'TestBootstrapCodecGoldenAndRoundTrip',
            'TestBootstrapCodecMaximumAndCopies',
            'TestBootstrapCodecMessageLength',
            'TestBootstrapCodecRejectsMalformedInput',
            'TestCanonicalIdentifiers',
            'TestCheckpointCodecDecoderCopiesAndInvalidConstruction',
            'TestCheckpointCodecGoldenAndCopies',
            'TestCheckpointCodecHeaderAndMalformedInput',
            'TestCheckpointCodecInvalidValuesAndNonceDigest',
            'TestCheckpointCodecPlanBoundsAndUnusedRows',
            'TestCheckpointCodecRequestSequenceLimits',
            'TestCheckpointCodecStickyRejectionAndNoPartialAdvance',
            'TestEventCodecRejectsMalformed',
            'TestEventCodecRoundTrip',
            'TestFrameCodecRejectsMalformed',
            'TestFrameCodecRoundTrip',
            'TestFrameMessageLengthClosedHeader',
            'TestLaunchValuesClosedBindings',
            'TestLaunchValuesPathLexicalBounds',
            'TestLaunchValuesScheduleAndArgv',
            'TestLaunchValuesWindowsDeviceNames',
            'TestLifecycleCodecClosedShapes',
            'TestLifecycleEventsShareCategoryAndStreamBounds',
            'TestRecorderBoundsAndInvalid',
            'TestRecorderCloseAndLate',
            'TestRecorderConcurrentWriters',
            'TestRecorderGapRemainsFailed',
            'TestRecorderPrefixAndCopies',
            'TestTranscriptBootBindingAndDigest',
            'TestTranscriptDecoderCheckpointAndEntryOrdering',
            'TestTranscriptDecoderCompleteCopies',
            'TestTranscriptDecoderCumulativeEventBounds',
            'TestTranscriptDecoderOutputAndCounterBounds',
            'TestTranscriptDecoderStickyEvidenceFailure',
            'TestTranscriptDecoderTerminalAndMalformed',
            'TestTranscriptPayloadCodecCanonical'],
  'timeoutSeconds': 130},
 {'argv': ['/tmp/sbp1-ci/bin/resourceacceptance.test',
           '-test.run=^(TestProcessEntryBootstrapPartialEOFNativePipe|TestProcessEntryBootstrapSingleReadNativePipe|TestProcessPipeNativeBlockedReadJoins|TestProcessPipeNativeBlockedWriteJoins|TestProcessPipeNativeConcurrentCloseOnce|TestProcessPipeNativeExactAndCloseOnce|TestProcessPipeNativePartialEOFIsFailure|TestProcessReporterNativeCheckpointAndSeal|TestProcessReporterNativeEOFBeforeFinalizeRejected|TestProcessReporterNativePrematureEOFIsFailure|TestProcessReporterNativeRejectsAliasingAndRebasedCutoff)$',
           '-test.v=true',
           '-test.count=1',
           '-test.parallel=1',
           '-test.shuffle=off',
           '-test.timeout=2m'],
  'id': 'pipes11',
  'package': 'github.com/webkaz-labs/sobalink/internal/resourceacceptance',
  'tests': ['TestProcessEntryBootstrapPartialEOFNativePipe',
            'TestProcessEntryBootstrapSingleReadNativePipe',
            'TestProcessPipeNativeBlockedReadJoins',
            'TestProcessPipeNativeBlockedWriteJoins',
            'TestProcessPipeNativeConcurrentCloseOnce',
            'TestProcessPipeNativeExactAndCloseOnce',
            'TestProcessPipeNativePartialEOFIsFailure',
            'TestProcessReporterNativeCheckpointAndSeal',
            'TestProcessReporterNativeEOFBeforeFinalizeRejected',
            'TestProcessReporterNativePrematureEOFIsFailure',
            'TestProcessReporterNativeRejectsAliasingAndRebasedCutoff'],
  'timeoutSeconds': 130},
 {'argv': ['/tmp/sbp1-ci/bin/resourceacceptance.test',
           '-test.run=^(TestProcessEntrySignalRegistrationJoinNativeSignal)$',
           '-test.v=true',
           '-test.count=1',
           '-test.parallel=1',
           '-test.shuffle=off',
           '-test.timeout=2m'],
  'id': 'signal1',
  'package': 'github.com/webkaz-labs/sobalink/internal/resourceacceptance',
  'tests': ['TestProcessEntrySignalRegistrationJoinNativeSignal'],
  'timeoutSeconds': 130},
 {'argv': ['/tmp/sbp1-ci/bin/resourceacceptance.test',
           '-test.run=^(TestProcessEntryCurrentImageIdentityNativeFilesystem|TestProcessEntryOwnedDirectoryNoFollowNativeFilesystem)$',
           '-test.v=true',
           '-test.count=1',
           '-test.parallel=1',
           '-test.shuffle=off',
           '-test.timeout=2m'],
  'id': 'filesystem2',
  'package': 'github.com/webkaz-labs/sobalink/internal/resourceacceptance',
  'tests': ['TestProcessEntryCurrentImageIdentityNativeFilesystem',
            'TestProcessEntryOwnedDirectoryNoFollowNativeFilesystem'],
  'timeoutSeconds': 130}]

COMMANDS = {'admit': {'argv': ['/usr/bin/python3',
                    '-I',
                    '-S',
                    '-B',
                    '.github/scripts/ci-resource-process-restart.py',
                    'admit'],
           'maxSeconds': 120},
 'build-product': {'argv': ['/opt/hostedtoolcache/go/1.27.1/x64/bin/go',
                            'build',
                            '-mod=readonly',
                            '-trimpath',
                            '-buildvcs=true',
                            '-pgo=off',
                            '-p=2',
                            '-tags=resource_process_native,directlan_activation_native,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy',
                            '<EXACT_SEVEN_STAMP_LDFLAGS>',
                            '-o',
                            '/tmp/sbp1-ci/bin/soba',
                            './cmd/soba'],
                   'maxSeconds': 420},
 'build-tests': {'argv': ['/opt/hostedtoolcache/go/1.27.1/x64/bin/go',
                          'test',
                          '-c',
                          '-mod=readonly',
                          '-trimpath',
                          '-buildvcs=true',
                          '-pgo=off',
                          '-p=2',
                          '-tags=resource_process_native,directlan_activation_native,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy',
                          '-o',
                          '/tmp/sbp1-ci/bin/',
                          './cmd/soba',
                          './internal/resourceacceptance',
                          './internal/resourceacceptance/processmodel'],
                 'maxSeconds': 480},
 'export': {'argv': ['/usr/bin/python3',
                     '-I',
                     '-S',
                     '-B',
                     '.github/scripts/ci-resource-process-restart.py',
                     'export'],
            'maxSeconds': 120},
 'harness-values': {'argv': ['/usr/bin/python3',
                             '-I',
                             '-S',
                             '-B',
                             '.github/scripts/test_ci_resource_process_restart.py'],
                    'maxSeconds': 120},
 'inspect-images': {'argv': ['/opt/hostedtoolcache/go/1.27.1/x64/bin/go',
                             'version',
                             '-m',
                             '/tmp/sbp1-ci/bin/soba',
                             '/tmp/sbp1-ci/bin/soba.test',
                             '/tmp/sbp1-ci/bin/resourceacceptance.test',
                             '/tmp/sbp1-ci/bin/processmodel.test'],
                    'maxSeconds': 30},
 'metadata': {'argv': ['/opt/hostedtoolcache/go/1.27.1/x64/bin/go',
                       'list',
                       '-mod=readonly',
                       '-deps',
                       '-test',
                       '-json',
                       '-tags=resource_process_native,directlan_activation_native,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy',
                       './cmd/soba',
                       './internal/resourceacceptance',
                       './internal/resourceacceptance/processmodel'],
              'maxSeconds': 120},
 'module-download': {'argv': ['/opt/hostedtoolcache/go/1.27.1/x64/bin/go', 'mod', 'download'],
                     'maxSeconds': 420},
 'module-verify': {'argv': ['/opt/hostedtoolcache/go/1.27.1/x64/bin/go', 'mod', 'verify'],
                   'maxSeconds': 120},
 'p1-native': {'argv': ['/tmp/sbp1-ci/bin/soba.test',
                        '-test.run=^TestResourceProcessControllerRestartKeepsHistoryNoReplay$',
                        '-test.v=true',
                        '-test.count=1',
                        '-test.parallel=1',
                        '-test.shuffle=off',
                        '-test.timeout=6m'],
               'maxSeconds': 360},
 'prepare-engine': {'argv': ['/opt/hostedtoolcache/go/1.27.1/x64/bin/go', 'run', './cmd/prepare-engine'],
                    'maxSeconds': 420},
 'verify-engine': {'argv': ['/opt/hostedtoolcache/go/1.27.1/x64/bin/go',
                            'run',
                            './cmd/prepare-engine',
                            '--verify'],
                   'maxSeconds': 120}}

BUILD_MODULE_RECORDS = {'processmodel.test': [['path',
                        'github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel.test'],
                       ['mod', 'github.com/webkaz-labs/sobalink', 'PUBLIC_PSEUDOVERSION', '']],
 'resourceacceptance.test': [['path', 'github.com/webkaz-labs/sobalink/internal/resourceacceptance.test'],
                             ['mod', 'github.com/webkaz-labs/sobalink', 'PUBLIC_PSEUDOVERSION', ''],
                             ['dep',
                              'golang.org/x/sys',
                              'v0.48.0',
                              'h1:bbX/i/6MgT9BVLM9RT1thmxL04yeTAhbEz4SyadbXoo=']],
 'soba': [['path', 'github.com/webkaz-labs/sobalink/cmd/soba'],
          ['mod', 'github.com/webkaz-labs/sobalink', 'PUBLIC_PSEUDOVERSION', ''],
          ['dep', 'charm.land/bubbletea/v2', 'v2.0.10', 'h1:oolvo20VBpI0PfqE7iFjkZ1bx0WpmXGfnKz5Yldjq5o='],
          ['dep', 'filippo.io/edwards25519', 'v1.2.0', 'h1:crnVqOiS4jqYleHd9vaKZ+HKtHfllngJIiOpNpoJsjo='],
          ['dep',
           'github.com/axiomhq/hyperloglog',
           'v0.2.6',
           'h1:sRhvvF3RIXWQgAXaTphLp4yJiX4S0IN3MWTaAgZoRJw='],
          ['dep',
           'github.com/charmbracelet/colorprofile',
           'v0.4.3',
           'h1:QPa1IWkYI+AOB+fE+mg/5/4HRMZcaXex9t5KX76i20Q='],
          ['dep',
           'github.com/charmbracelet/ultraviolet',
           'v0.0.0-20261001125412-878653296cfd',
           'h1:W0HOPGkwRaAmV20plE/f+Rk6Xn+IdR8G0qHsGXcOnF8='],
          ['dep',
           'github.com/charmbracelet/x/ansi',
           'v0.11.8',
           'h1:JMFwp0CgDC2+jcOB162HH5k7I3FVbgFSMMYg7dSPBQQ='],
          ['dep',
           'github.com/charmbracelet/x/term',
           'v0.2.2',
           'h1:xVRT/S2ZcKdhhOuSP4t5cLi5o+JxklsoEObBSgfgZRk='],
          ['dep',
           'github.com/charmbracelet/x/termios',
           'v0.1.1',
           'h1:o3Q2bT8eqzGnGPOYheoYS8eEleT5ZVNYNy8JawjaNZY='],
          ['dep',
           'github.com/charmbracelet/x/windows',
           'v0.2.2',
           'h1:IofanmuvaxnKHuV04sC0eBy/smG6kIKrWG2/jYn2GuM='],
          ['dep',
           'github.com/clipperhouse/displaywidth',
           'v0.11.0',
           'h1:lBc6kY44VFw+TDx4I8opi/EtL9m20WSEFgwIwO+UVM8='],
          ['dep',
           'github.com/clipperhouse/uax29/v2',
           'v2.7.0',
           'h1:+gs4oBZ2gPfVrKPthwbMzWZDaAFPGYK72F0NJv2v7Vk='],
          ['dep',
           'github.com/coder/websocket',
           'v1.8.15',
           'h1:6B2JPeOGlpff2Uz6vOEH1Vzpi0iUz20A+lPVhPHtNUA='],
          ['dep',
           'github.com/creachadair/msync',
           'v0.10.1',
           'h1:14jT5Ujob64Zp2D3ZcUidayDMoXikhmYN8xBxEuPKPk='],
          ['dep',
           'github.com/dgryski/go-metro',
           'v0.0.0-20250106013310-edb8663e5e33',
           'h1:ucRHb6/lvW/+mTEIGbvhcYU3S8+uSNkuMjx/qZFfhtM='],
          ['dep',
           'github.com/fxamacker/cbor/v2',
           'v2.9.3',
           'h1:oQBnFATpNdY8gJHTndDDv5Xl4QqNaz51G5LLEPhng3Q='],
          ['dep', 'github.com/gaissmai/bart', 'v0.29.0', 'h1:wO6HGE8g9YE0Wm0bCpYxwRzfQ4+fbJKOhL64e5ACGCI='],
          ['dep',
           'github.com/go-json-experiment/json',
           'v0.0.0-20260820222146-c27c302e5fc3',
           'h1:UADEEmDKgfXbtnGJZ97beY5XLo9ZechG1nlU4KnRrkE='],
          ['dep',
           'github.com/go4org/hashtriemap',
           'v0.0.0-20260925222741-44e5305f85d9',
           'h1:J8S+Ms89nvHX1F9Ma2m3KhdTl41ZhbFKMyAJqvDA+iE='],
          ['dep', 'github.com/godbus/dbus/v5', 'v5.2.2', 'h1:TUR3TgtSVDmjiXOgAAyaZbYmIeP3DPkld3jgKGV8mXQ='],
          ['dep',
           'github.com/golang/groupcache',
           'v0.0.0-20241129210726-2c02b8208cf8',
           'h1:f+oWsMOmNPc8JmEHVZIycC7hBoQxHH9pNKQORJNozsQ='],
          ['dep', 'github.com/google/btree', 'v1.1.3', 'h1:CVpQJjYgC4VbzxeGVHfvZrv1ctoYCAI8vbl07Fcxlyg='],
          ['dep',
           'github.com/hdevalence/ed25519consensus',
           'v0.2.0',
           'h1:37ICyZqdyj0lAZ8P4D1d1id3HqbbG1N3iBb1Tb4rdcU='],
          ['dep',
           'github.com/jsimonetti/rtnetlink',
           'v1.4.2',
           'h1:Df9w9TZ3npHTyDn0Ev9e1uzmN2odmXd0QX+J5GTEn90='],
          ['dep',
           'github.com/kamstrup/intmap',
           'v0.5.2',
           'h1:qnwBm1mh4XAnW9W9Ue9tZtTff8pS6+s6iKF6JRIV2Dk='],
          ['dep',
           'github.com/klauspost/compress',
           'v1.20.0',
           'h1:a3C1ke2ohxFymNlb2HWAHjDeKCI90scRskErZkR0ezA='],
          ['dep',
           'github.com/lucasb-eyer/go-colorful',
           'v1.4.1',
           'h1:1EO+WB73+EH8EVbzlrG3KLAfEypQWVHIBqlTf+2hNss='],
          ['dep',
           'github.com/mattn/go-runewidth',
           'v0.0.30',
           'h1:+KUuiDA4fF0R1p5FeueHefjDm+GIM+kWfFnDjybOPgk='],
          ['dep',
           'github.com/mdlayher/netlink',
           'v1.11.2',
           'h1:HKh2jqe+omdSWcQ88nrT7INE61B0NXfiSPFdgL4YbNI='],
          ['dep',
           'github.com/mdlayher/socket',
           'v0.7.0',
           'h1:qVREPVwtUMg17pwvveQxpurq0PVisMxi3FGgpsorMYQ='],
          ['dep',
           'github.com/mitchellh/go-ps',
           'v1.0.0',
           'h1:i6ampVEEF4wQFF+bkYfwYgY+F/uYJDktmvLPf7qIgjc='],
          ['dep',
           'github.com/muesli/cancelreader',
           'v0.2.2',
           'h1:3I4Kt4BQjOR54NavqnDogx/MIoWBFa0StPA8ELUXHmA='],
          ['dep',
           'github.com/pires/go-proxyproto',
           'v0.15.0',
           'h1:dTshmNbFm/D+0+sbrxUuddPOZ5Y0B7c5NhtsBkm6LqI='],
          ['dep', 'github.com/rivo/uniseg', 'v0.4.7', 'h1:WUdvkW8uEhrYfLC4ZzdpI2ztxP1I582+49Oc5Mq64VQ='],
          ['dep',
           'github.com/safchain/ethtool',
           'v0.7.0',
           'h1:rlJzfDetsVvT61uz8x1YIcFn12akMfuPulHtZjtb7Is='],
          ['dep',
           'github.com/skip2/go-qrcode',
           'v0.0.0-20200617195104-da1b6568686e',
           'h1:MRM5ITcdelLK2j1vwZ3Je0FKVCfqOLp5zO6trqMLYs0='],
          ['dep',
           'github.com/tailscale/hujson',
           'v0.0.0-20260727124030-b80ff77dac4f',
           'h1:9hiVElpCmKzsBKQHkBqZ8LGzt82iLfM8egxr4sew+Ys='],
          ['dep',
           'github.com/tailscale/peercred',
           'v0.0.0-20250107143737-35a0c7bd7edc',
           'h1:24heQPtnFR+yfntqhI3oAu9i27nEojcQ4NuBQOo5ZFA='],
          ['dep',
           'github.com/tailscale/web-client-prebuilt',
           'v0.0.0-20260917222731-e0ed2d0d0fea',
           'h1:ah5jt6ZnxMvNclt6kBBoLcMpLLjMA44PTsnov2y7gpU='],
          ['dep', 'github.com/tailscale/wireguard-go', 'v0.0.0-20260928213032-417aef361226'],
          ['=>', './.sobalink-deps/wireguard', '(devel)', ''],
          ['dep', 'github.com/x448/float16', 'v0.8.4', 'h1:qLwI1I70+NjRFUR3zs1JPUCgaCXSh3SW62uAKT1mSBM='],
          ['dep', 'github.com/xo/terminfo', 'v1.0.0', 'h1:2ZpYzqWzyyytjk3TP6aJVDhkMAkc99/1xKQdA3TDTBY='],
          ['dep',
           'go4.org/mem',
           'v0.0.0-20240501181205-ae6ca9944745',
           'h1:Tl++JLUCe4sxGu8cTpDzRLd3tN7US4hOxG5YpKCzkek='],
          ['dep',
           'go4.org/netipx',
           'v0.0.0-20260823151212-3075585bcbeb',
           'h1:XBM4hvfwGAttkkiTIFfeigdfcL1xIfdKXqFdgiHGtDs='],
          ['dep', 'golang.org/x/crypto', 'v0.57.0', 'h1:3ZVCjf8Ggz7zneR/EHRVx68Ctf+2pmIMP2UFhh9cC6M='],
          ['dep',
           'golang.org/x/exp',
           'v0.0.0-20260908205506-85c1c2202aba',
           'h1:Ck8QetSgk912qxWLMCKxd0in+aiyBQyDSMae6e/xmpU='],
          ['dep', 'golang.org/x/net', 'v0.59.0', 'h1:5zfYln+w5XCxwrnMMJPufRgNoXEaGxl0wo5GqPXyues='],
          ['dep', 'golang.org/x/oauth2', 'v0.37.0', 'h1:JUlcxA8oAtauLfiH8FX2/FkAWHAdi0QtGCGc+hofE98='],
          ['dep', 'golang.org/x/sync', 'v0.23.0', 'h1:KameEIfc1IkluZyXWLn39Wd4tURc6GbCiISGiZm2bQk='],
          ['dep', 'golang.org/x/sys', 'v0.48.0', 'h1:bbX/i/6MgT9BVLM9RT1thmxL04yeTAhbEz4SyadbXoo='],
          ['dep', 'golang.org/x/term', 'v0.46.0', 'h1:3+OXuTbaKDgwk8jTi3aSLHRlmWqHEUDUtxnbFigO4YE='],
          ['dep', 'golang.org/x/text', 'v0.42.0', 'h1:JbOZXgfeCPU9gacVtYliJqOhD+zhrEqK4LfdpmlUZqI='],
          ['dep', 'golang.org/x/time', 'v0.16.0', 'h1:vMb6ptszcQMkcwiRTAuNNU50gom6++Q/6gY2hDM6VDE='],
          ['dep', 'gvisor.dev/gvisor', 'v0.0.0-20260915211658-a6f909f08a72'],
          ['=>', './.sobalink-deps/gvisor', '(devel)', ''],
          ['dep', 'tailscale.com', 'v1.104.0'],
          ['=>', './.sobalink-deps/tailscale', '(devel)', '']],
 'soba.test': [['path', 'github.com/webkaz-labs/sobalink/cmd/soba.test'],
               ['mod', 'github.com/webkaz-labs/sobalink', 'PUBLIC_PSEUDOVERSION', ''],
               ['dep',
                'charm.land/bubbletea/v2',
                'v2.0.10',
                'h1:oolvo20VBpI0PfqE7iFjkZ1bx0WpmXGfnKz5Yldjq5o='],
               ['dep',
                'filippo.io/edwards25519',
                'v1.2.0',
                'h1:crnVqOiS4jqYleHd9vaKZ+HKtHfllngJIiOpNpoJsjo='],
               ['dep',
                'github.com/axiomhq/hyperloglog',
                'v0.2.6',
                'h1:sRhvvF3RIXWQgAXaTphLp4yJiX4S0IN3MWTaAgZoRJw='],
               ['dep',
                'github.com/charmbracelet/colorprofile',
                'v0.4.3',
                'h1:QPa1IWkYI+AOB+fE+mg/5/4HRMZcaXex9t5KX76i20Q='],
               ['dep',
                'github.com/charmbracelet/ultraviolet',
                'v0.0.0-20261001125412-878653296cfd',
                'h1:W0HOPGkwRaAmV20plE/f+Rk6Xn+IdR8G0qHsGXcOnF8='],
               ['dep',
                'github.com/charmbracelet/x/ansi',
                'v0.11.8',
                'h1:JMFwp0CgDC2+jcOB162HH5k7I3FVbgFSMMYg7dSPBQQ='],
               ['dep',
                'github.com/charmbracelet/x/term',
                'v0.2.2',
                'h1:xVRT/S2ZcKdhhOuSP4t5cLi5o+JxklsoEObBSgfgZRk='],
               ['dep',
                'github.com/charmbracelet/x/termios',
                'v0.1.1',
                'h1:o3Q2bT8eqzGnGPOYheoYS8eEleT5ZVNYNy8JawjaNZY='],
               ['dep',
                'github.com/charmbracelet/x/windows',
                'v0.2.2',
                'h1:IofanmuvaxnKHuV04sC0eBy/smG6kIKrWG2/jYn2GuM='],
               ['dep',
                'github.com/clipperhouse/displaywidth',
                'v0.11.0',
                'h1:lBc6kY44VFw+TDx4I8opi/EtL9m20WSEFgwIwO+UVM8='],
               ['dep',
                'github.com/clipperhouse/uax29/v2',
                'v2.7.0',
                'h1:+gs4oBZ2gPfVrKPthwbMzWZDaAFPGYK72F0NJv2v7Vk='],
               ['dep',
                'github.com/coder/websocket',
                'v1.8.15',
                'h1:6B2JPeOGlpff2Uz6vOEH1Vzpi0iUz20A+lPVhPHtNUA='],
               ['dep',
                'github.com/creachadair/msync',
                'v0.10.1',
                'h1:14jT5Ujob64Zp2D3ZcUidayDMoXikhmYN8xBxEuPKPk='],
               ['dep',
                'github.com/dgryski/go-metro',
                'v0.0.0-20250106013310-edb8663e5e33',
                'h1:ucRHb6/lvW/+mTEIGbvhcYU3S8+uSNkuMjx/qZFfhtM='],
               ['dep',
                'github.com/fxamacker/cbor/v2',
                'v2.9.3',
                'h1:oQBnFATpNdY8gJHTndDDv5Xl4QqNaz51G5LLEPhng3Q='],
               ['dep',
                'github.com/gaissmai/bart',
                'v0.29.0',
                'h1:wO6HGE8g9YE0Wm0bCpYxwRzfQ4+fbJKOhL64e5ACGCI='],
               ['dep',
                'github.com/go-json-experiment/json',
                'v0.0.0-20260820222146-c27c302e5fc3',
                'h1:UADEEmDKgfXbtnGJZ97beY5XLo9ZechG1nlU4KnRrkE='],
               ['dep',
                'github.com/go4org/hashtriemap',
                'v0.0.0-20260925222741-44e5305f85d9',
                'h1:J8S+Ms89nvHX1F9Ma2m3KhdTl41ZhbFKMyAJqvDA+iE='],
               ['dep',
                'github.com/godbus/dbus/v5',
                'v5.2.2',
                'h1:TUR3TgtSVDmjiXOgAAyaZbYmIeP3DPkld3jgKGV8mXQ='],
               ['dep',
                'github.com/golang/groupcache',
                'v0.0.0-20241129210726-2c02b8208cf8',
                'h1:f+oWsMOmNPc8JmEHVZIycC7hBoQxHH9pNKQORJNozsQ='],
               ['dep',
                'github.com/google/btree',
                'v1.1.3',
                'h1:CVpQJjYgC4VbzxeGVHfvZrv1ctoYCAI8vbl07Fcxlyg='],
               ['dep',
                'github.com/hdevalence/ed25519consensus',
                'v0.2.0',
                'h1:37ICyZqdyj0lAZ8P4D1d1id3HqbbG1N3iBb1Tb4rdcU='],
               ['dep',
                'github.com/jsimonetti/rtnetlink',
                'v1.4.2',
                'h1:Df9w9TZ3npHTyDn0Ev9e1uzmN2odmXd0QX+J5GTEn90='],
               ['dep',
                'github.com/kamstrup/intmap',
                'v0.5.2',
                'h1:qnwBm1mh4XAnW9W9Ue9tZtTff8pS6+s6iKF6JRIV2Dk='],
               ['dep',
                'github.com/klauspost/compress',
                'v1.20.0',
                'h1:a3C1ke2ohxFymNlb2HWAHjDeKCI90scRskErZkR0ezA='],
               ['dep',
                'github.com/lucasb-eyer/go-colorful',
                'v1.4.1',
                'h1:1EO+WB73+EH8EVbzlrG3KLAfEypQWVHIBqlTf+2hNss='],
               ['dep',
                'github.com/mattn/go-runewidth',
                'v0.0.30',
                'h1:+KUuiDA4fF0R1p5FeueHefjDm+GIM+kWfFnDjybOPgk='],
               ['dep',
                'github.com/mdlayher/netlink',
                'v1.11.2',
                'h1:HKh2jqe+omdSWcQ88nrT7INE61B0NXfiSPFdgL4YbNI='],
               ['dep',
                'github.com/mdlayher/socket',
                'v0.7.0',
                'h1:qVREPVwtUMg17pwvveQxpurq0PVisMxi3FGgpsorMYQ='],
               ['dep',
                'github.com/mitchellh/go-ps',
                'v1.0.0',
                'h1:i6ampVEEF4wQFF+bkYfwYgY+F/uYJDktmvLPf7qIgjc='],
               ['dep',
                'github.com/muesli/cancelreader',
                'v0.2.2',
                'h1:3I4Kt4BQjOR54NavqnDogx/MIoWBFa0StPA8ELUXHmA='],
               ['dep',
                'github.com/pires/go-proxyproto',
                'v0.15.0',
                'h1:dTshmNbFm/D+0+sbrxUuddPOZ5Y0B7c5NhtsBkm6LqI='],
               ['dep',
                'github.com/rivo/uniseg',
                'v0.4.7',
                'h1:WUdvkW8uEhrYfLC4ZzdpI2ztxP1I582+49Oc5Mq64VQ='],
               ['dep',
                'github.com/safchain/ethtool',
                'v0.7.0',
                'h1:rlJzfDetsVvT61uz8x1YIcFn12akMfuPulHtZjtb7Is='],
               ['dep',
                'github.com/skip2/go-qrcode',
                'v0.0.0-20200617195104-da1b6568686e',
                'h1:MRM5ITcdelLK2j1vwZ3Je0FKVCfqOLp5zO6trqMLYs0='],
               ['dep',
                'github.com/tailscale/hujson',
                'v0.0.0-20260727124030-b80ff77dac4f',
                'h1:9hiVElpCmKzsBKQHkBqZ8LGzt82iLfM8egxr4sew+Ys='],
               ['dep',
                'github.com/tailscale/peercred',
                'v0.0.0-20250107143737-35a0c7bd7edc',
                'h1:24heQPtnFR+yfntqhI3oAu9i27nEojcQ4NuBQOo5ZFA='],
               ['dep',
                'github.com/tailscale/web-client-prebuilt',
                'v0.0.0-20260917222731-e0ed2d0d0fea',
                'h1:ah5jt6ZnxMvNclt6kBBoLcMpLLjMA44PTsnov2y7gpU='],
               ['dep', 'github.com/tailscale/wireguard-go', 'v0.0.0-20260928213032-417aef361226'],
               ['=>', './.sobalink-deps/wireguard', '(devel)', ''],
               ['dep',
                'github.com/x448/float16',
                'v0.8.4',
                'h1:qLwI1I70+NjRFUR3zs1JPUCgaCXSh3SW62uAKT1mSBM='],
               ['dep',
                'github.com/xo/terminfo',
                'v1.0.0',
                'h1:2ZpYzqWzyyytjk3TP6aJVDhkMAkc99/1xKQdA3TDTBY='],
               ['dep',
                'go4.org/mem',
                'v0.0.0-20240501181205-ae6ca9944745',
                'h1:Tl++JLUCe4sxGu8cTpDzRLd3tN7US4hOxG5YpKCzkek='],
               ['dep',
                'go4.org/netipx',
                'v0.0.0-20260823151212-3075585bcbeb',
                'h1:XBM4hvfwGAttkkiTIFfeigdfcL1xIfdKXqFdgiHGtDs='],
               ['dep', 'golang.org/x/crypto', 'v0.57.0', 'h1:3ZVCjf8Ggz7zneR/EHRVx68Ctf+2pmIMP2UFhh9cC6M='],
               ['dep',
                'golang.org/x/exp',
                'v0.0.0-20260908205506-85c1c2202aba',
                'h1:Ck8QetSgk912qxWLMCKxd0in+aiyBQyDSMae6e/xmpU='],
               ['dep', 'golang.org/x/net', 'v0.59.0', 'h1:5zfYln+w5XCxwrnMMJPufRgNoXEaGxl0wo5GqPXyues='],
               ['dep', 'golang.org/x/oauth2', 'v0.37.0', 'h1:JUlcxA8oAtauLfiH8FX2/FkAWHAdi0QtGCGc+hofE98='],
               ['dep', 'golang.org/x/sync', 'v0.23.0', 'h1:KameEIfc1IkluZyXWLn39Wd4tURc6GbCiISGiZm2bQk='],
               ['dep', 'golang.org/x/sys', 'v0.48.0', 'h1:bbX/i/6MgT9BVLM9RT1thmxL04yeTAhbEz4SyadbXoo='],
               ['dep', 'golang.org/x/term', 'v0.46.0', 'h1:3+OXuTbaKDgwk8jTi3aSLHRlmWqHEUDUtxnbFigO4YE='],
               ['dep', 'golang.org/x/text', 'v0.42.0', 'h1:JbOZXgfeCPU9gacVtYliJqOhD+zhrEqK4LfdpmlUZqI='],
               ['dep', 'golang.org/x/time', 'v0.16.0', 'h1:vMb6ptszcQMkcwiRTAuNNU50gom6++Q/6gY2hDM6VDE='],
               ['dep', 'gvisor.dev/gvisor', 'v0.0.0-20260915211658-a6f909f08a72'],
               ['=>', './.sobalink-deps/gvisor', '(devel)', ''],
               ['dep', 'tailscale.com', 'v1.104.0'],
               ['=>', './.sobalink-deps/tailscale', '(devel)', '']]}

ROOT, OUT, BASE, GOROOT = map(pathlib.Path, (ROOT, OUT, BASE, GOROOT))
GO = str(GOROOT / 'bin/go')
GIT = '/usr/bin/git'
TAGS = 'resource_process_native,directlan_activation_native,ts_omit_portmapper,ts_omit_captiveportal,ts_omit_useproxy'
TEST = 'TestResourceProcessControllerRestartKeepsHistoryNoReplay'
ANCHOR = ROOT / '.github/fixtures/resource-process-restart/reviewed-inputs.json'
UNJOINED = ['start', 'exit', 'wait', 'stdout', 'stderr', 'writer', 'handle', 'driver', 'reservation']
IMAGE_ROLES = ['soba', 'soba.test', 'resourceacceptance.test', 'processmodel.test']
# Only run_native initializes the globals consumed by the preserved P1 helpers.
SPEC = {}

def require(condition, category):
    if not condition:
        raise RuntimeError(category)


def sha(path):
    digest = hashlib.sha256()
    with pathlib.Path(path).open('rb') as stream:
        for block in iter(lambda: stream.read(524288), b''):
            digest.update(block)
    return digest.hexdigest()


def encoded(value):
    return (json.dumps(value, indent=2, sort_keys=True) + '\n').encode('utf-8')


def private_directory(path, retain=True):
    facts = path.lstat()
    require(stat.S_ISDIR(facts.st_mode) and stat.S_IMODE(facts.st_mode) == 0o700
            and facts.st_uid == os.geteuid(), 'scratch_parent_not_private')
    identity = (facts.st_dev, facts.st_ino)
    if path in private_identities:
        require(private_identities[path] == identity, 'scratch_parent_replaced')
    elif retain:
        private_identities[path] = identity
    return facts


def fixture_root_binding():
    """Filesystem ownership only; never process discovery or a PID authority."""
    global fixture_root, fixture_root_absent
    private_directory(BASE)
    private_directory(BASE / 'home')
    private_directory(BASE / 'tmp')
    candidates = list((BASE / 'tmp').iterdir())
    require(len(candidates) <= 1, 'multiple_fixture_roots')
    if fixture_root is not None:
        if not candidates:
            fixture_root_absent = True
            return
        require(not fixture_root_absent and candidates == [fixture_root], 'fixture_root_reappeared_or_changed')
    elif candidates:
        candidate = candidates[0]
        require(re.fullmatch(r'p1-(?:0|[1-9][0-9]{0,9})', candidate.name) is not None
                and int(candidate.name[3:]) <= 4294967295, 'unexpected_fixture_root_name')
        private_directory(candidate)
        fixture_root = candidate
    if fixture_root is not None and not fixture_root_absent:
        try:
            private_directory(fixture_root)
        except FileNotFoundError as exc:
            require(exc.errno == errno.ENOENT, 'fixture_root_stat_failed')
            fixture_root_absent = True


def permitted_socket(path, item):
    require(fixture_root is not None and not fixture_root_absent, 'socket_without_retained_fixture')
    allowed = [fixture_root / name for name in SPEC['socketException']['relativePaths']]
    require(path in allowed and len(os.fsencode(path)) <= 100, 'unexpected_socket_path')
    require(stat.S_ISSOCK(item.st_mode) and item.st_uid == os.geteuid()
            and stat.S_IMODE(item.st_mode) in (0o600, 0o700), 'socket_not_private')
    # lstat never follows a symlink. These are the exact retained real parents.
    for parent in [fixture_root, fixture_root / 'state', path.parent]:
        private_directory(parent)
    require(not fixture_root_absent, 'socket_after_fixture_removal')
    observed_socket_paths.add(str(path))


def scratch_bytes(root):
    """Reviewed lstat sampler, with only three exact private socket exceptions.

    The sampled facts are not a race-proof filesystem sandbox or an OS quota.
    Terminal fixture disappearance is allowed, never replacement/rebinding.
    """
    global sample_disappearances, fixture_root_absent
    first = root.lstat()
    require(stat.S_ISDIR(first.st_mode) and (first.st_dev, first.st_ino) == scratch_root_identity,
            'scratch_root_identity_changed')
    fixture_root_binding()

    def walk_error(exc):
        global sample_disappearances
        path = pathlib.Path(exc.filename) if exc.filename is not None else None
        if (isinstance(exc, FileNotFoundError) and exc.errno == errno.ENOENT and path is not None
                and path != root and path.is_relative_to(root)):
            sample_disappearances += 1
            return
        raise exc

    total, count = 0, 0
    for parent, dirs, files in os.walk(root, followlinks=False, onerror=walk_error):
        for name in dirs + files:
            path = pathlib.Path(parent) / name
            require(path != root and path.is_relative_to(root), 'scratch_non_descendant')
            count += 1
            require(count <= SPEC['scratchEntries'], 'scratch_entry_limit')
            try:
                item = path.lstat()
                require(not stat.S_ISLNK(item.st_mode), 'scratch_symlink')
                if stat.S_ISREG(item.st_mode):
                    total += item.st_size
                elif stat.S_ISDIR(item.st_mode):
                    if fixture_root is not None and path in [fixture_root, fixture_root / 'state',
                            fixture_root / 'state/c', fixture_root / 'state/a', fixture_root / 'state/b']:
                        private_directory(path)
                elif stat.S_ISSOCK(item.st_mode):
                    permitted_socket(path, item)
                else:
                    raise RuntimeError('scratch_special_file')
            except FileNotFoundError as exc:
                # Ordinary atomic file replacement and successful fixture teardown
                # may remove a lexical descendant between lstat observations.
                require(exc.errno == errno.ENOENT, 'scratch_stat_error')
                sample_disappearances += 1
                continue
    last = root.lstat()
    require(stat.S_ISDIR(last.st_mode) and (last.st_dev, last.st_ino) == (first.st_dev, first.st_ino),
            'scratch_root_changed')
    fixture_root_binding()
    return total


def fail(category):
    with lock:
        if category not in receipt['failureCategories']:
            receipt['failureCategories'].append(category)
        if receipt['firstFailure'] is None:
            receipt['firstFailure'] = {'category': category, 'phase': phase,
                                       'elapsedSeconds': round(time.monotonic() - command_started, 6)}


def capture(pipe, destination):
    try:
        with destination.open('xb') as sink:
            while True:
                data = pipe.read(8192)
                if not data:
                    break
                observed = time.monotonic()
                with lock:
                    state['seen'] += len(data)
                    state['lastOutputObservedAt'] = max(state['lastOutputObservedAt'], observed)
                    if observed >= command_deadline:
                        state['lateOutput'] = True
                        fail('output_observed_after_outer_deadline')
                    if state['captureFailure'] is not None:
                        continue
                    if state['written'] + len(data) > SPEC['rawOutputBytes']:
                        state['captureFailure'] = 'output_overflow'
                        fail('output_overflow')
                        continue
                    # Quota is checked while holding the shared lock before write.
                    sink.write(data)
                    state['written'] += len(data)
    except BaseException:
        with lock:
            if state['captureFailure'] is None:
                state['captureFailure'] = 'capture_failed'
            fail('capture_failed')
    finally:
        try:
            pipe.close()
        except BaseException:
            fail('capture_pipe_close_failed')


def sweep_owned_group():
    # A single unconditional containment sweep is separate from application joins.
    # Only the Popen spawn and retained WNOWAIT leader authorize this exact group.
    require(proc is not None and leader_unreaped, 'owned_leader_not_retained')
    if receipt['groupSignalAttempted']:
        return
    receipt['groupSignalAttempted'] = True
    try:
        os.killpg(proc.pid, signal.SIGKILL)
        receipt['groupSignals'] += 1
    except ProcessLookupError:
        receipt['retainedGroupAlreadyAbsent'] = True
    except BaseException:
        fail('retained_group_signal_failed')
        raise


def group_exists():
    try:
        os.killpg(proc.pid, 0)  # Read-only existence only, including after reap.
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        return True  # Ambiguous identity/access is failed cleanup, never authority.


def join_owned():
    global leader_unreaped
    require(cleanup_started is not None, 'cleanup_deadline_missing')
    reap_until = cleanup_started + SPEC['processReapMarginSeconds']
    join_until = reap_until + SPEC['readerAndGroupJoinMarginSeconds']
    while os.waitid(os.P_PID, proc.pid, os.WEXITED | os.WNOHANG | os.WNOWAIT) is None:
        if time.monotonic() >= reap_until:
            fail('supervisor_process_unjoined')
            return
        time.sleep(0.02)
    if time.monotonic() >= reap_until:
        fail('supervisor_reap_deadline_exceeded')
    # Clear before ANY reap attempt. Exception paths can never reopen identity.
    leader_unreaped = False
    try:
        receipt['returncode'] = proc.wait(timeout=max(0.001, reap_until - time.monotonic()))
        receipt['processJoined'] = True
    except BaseException:
        fail('supervisor_reap_unproven')
        return
    while group_exists() and time.monotonic() < join_until:
        time.sleep(0.05)
    receipt['ownedGroupAbsent'] = not group_exists()
    for item in readers:
        if item['started']:
            item['thread'].join(timeout=max(0, join_until - time.monotonic()))
    receipt['readersJoined'] = len(readers) == 2 and all(
        item['started'] and not item['thread'].is_alive() for item in readers)
    if not receipt['ownedGroupAbsent']:
        fail('supervisor_group_unjoined')
    if not receipt['readersJoined']:
        fail('supervisor_readers_unjoined')
    if time.monotonic() > join_until:
        fail('cleanup_join_deadline_exceeded')


def no_duplicate_object(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, 'duplicate_receipt_key')
        result[key] = value
    return result


def validate_fixture_stdout(path):
    lines = path.read_text(encoding='utf-8').splitlines()
    test = SPEC['receiptRules']['exactTest']
    require(len(lines) == 4 and lines[0] == '=== RUN   ' + test and lines[3] == 'PASS',
            'extra_missing_or_invalid_test_output')
    require(re.fullmatch(r'--- PASS: ' + test + r' \([0-9]+(?:\.[0-9]+)?s\)', lines[2]) is not None,
            'missing_exact_single_pass')
    match = re.fullmatch(r'    resource_process_acceptance_native_test.go:[0-9]+: (\{.*\})', lines[1])
    require(match is not None and len(match.group(1).encode()) <= 16384, 'fixture_receipt_missing_or_large')
    actual = json.loads(match.group(1), object_pairs_hook=no_duplicate_object)
    required = {
        'scenario': 'source-built-controller-restart-v1',
        'source': SPEC['source']['commit'], 'tree': SPEC['source']['tree'],
        'sourceManifest': manifest['sourceManifestSHA256'], 'binary': manifest['binarySHA256'],
        'assets': manifest['assetSHA256'], 'dependencies': manifest['dependencyManifestSHA256'],
        'toolchain': manifest['toolchainSHA256'], 'target': 'linux-amd64',
        'selector': '^' + test + '$', 'outcome': 'passed', 'stage': '',
        'owners': 5, 'clis': 40, 'checkpoints': 26,
        'originalExpiryPreserved': True, 'stableIdPreserved': True,
        'historyEqual': True, 'noReplay': True, 'joined': True,
        'evidenceScope': 'instrumented source-built Linux entry only; no installed-binary or other-target acceptance',
        'receiveState': 'legacy_review_required; no transfer acceptance or repair',
        'unjoined': {key: 0 for key in SPEC['receiptRules']['allUnjoinedZero']},
    }
    require(set(actual) == set(required) | {'maintenance'}, 'fixture_receipt_field_set')
    for key, expected_value in required.items():
        require(type(actual[key]) is type(expected_value) and actual[key] == expected_value,
                'fixture_receipt_mismatch_' + key.lower())
    require(type(actual['maintenance']) is int and actual['maintenance'] >= 2, 'fixture_maintenance_incomplete')
    require(all(type(actual['unjoined'][key]) is int for key in required['unjoined']),
            'fixture_unjoined_counter_type')
    return actual


def strict_json(raw):
    return json.loads(raw, object_pairs_hook=no_duplicate_object,
                      parse_constant=lambda value: (_ for _ in ()).throw(RuntimeError('invalid_json_number')))


def regular_bytes(path, maximum=16 << 20):
    path = pathlib.Path(path)
    for parent in reversed(path.parents):
        require(not stat.S_ISLNK(parent.lstat().st_mode), 'linked_input_parent')
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
    try:
        facts = os.fstat(fd)
        require(stat.S_ISREG(facts.st_mode) and facts.st_size <= maximum, 'input_shape_or_size')
        with os.fdopen(fd, 'rb', closefd=False) as stream:
            raw = stream.read(maximum + 1)
        require(len(raw) == facts.st_size and len(raw) <= maximum, 'input_size_changed')
        return raw
    finally:
        os.close(fd)


def save_new(path, value, maximum=16 << 20):
    raw = value if type(value) is bytes else encoded(value)
    require(len(raw) <= maximum, 'new_file_overflow')
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
    with os.fdopen(fd, 'wb') as stream:
        stream.write(raw)


def git(*args):
    result = subprocess.run(
        [GIT, '-c', 'core.fsmonitor=false', '-c', 'core.hooksPath=/dev/null',
         '-c', 'gc.auto=0', '-c', 'maintenance.auto=false', '-C', str(ROOT), *args],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=15, check=False,
        env={'PATH': '/usr/bin:/bin', 'HOME': '/nonexistent', 'LANG': 'C', 'LC_ALL': 'C',
             'GIT_CONFIG_NOSYSTEM': '1', 'GIT_CONFIG_GLOBAL': '/dev/null', 'GIT_OPTIONAL_LOCKS': '0'})
    require(result.returncode == 0 and len(result.stdout) <= 4 << 20 and len(result.stderr) <= 65536,
            'git_metadata_failed')
    return result.stdout.decode('utf-8').strip()


def safe_relative(name):
    require(type(name) is str and name and len(name) <= 1024, 'invalid_relative_path')
    p = pathlib.PurePosixPath(name)
    require(not p.is_absolute() and p.as_posix() == name and '..' not in p.parts
            and not any(c in name for c in '\\\x00\r\n\t:'), 'invalid_relative_path')
    return name


def load_anchor():
    raw = regular_bytes(ANCHOR, 5 << 20)
    require(hashlib.sha256(raw).hexdigest() == ANCHOR_SHA256, 'anchor_changed')
    anchor = strict_json(raw)
    require(anchor['reviewedProductCommit'] == BASELINE_COMMIT
            and anchor['reviewedProductTree'] == BASELINE_TREE, 'baseline_changed')
    require(len(anchor['baselineTrackedFiles']) == 1470 and len(anchor['toolchainFiles']) == 15639
            and len(anchor['dependencyFiles']) == 7536 and len(anchor['activeInputs']) == 3642
            and len(anchor['generatedTestMainFiles']) == 3, 'anchor_count_changed')
    for key, digest in [('toolchainFiles', TOOLCHAIN_SHA256), ('dependencyFiles', DEPENDENCY_SHA256),
                        ('activeInputs', ACTIVE_SHA256)]:
        require(hashlib.sha256(encoded(anchor[key])).hexdigest() == digest, 'portable_digest_changed')
    return anchor


def source_inventory(anchor):
    records = {}
    for line in git('ls-tree', '-r', '--full-tree', 'HEAD').splitlines():
        meta, name = line.split('\t', 1)
        safe_relative(name)
        mode, kind, oid = meta.split()
        require(kind == 'blob' and mode in ('100644', '100755') and name not in records,
                'tracked_source_shape')
        path = ROOT / name
        raw = regular_bytes(path, 16 << 20)
        require(('100755' if path.stat().st_mode & 0o111 else '100644') == mode, 'source_mode_changed')
        require(hashlib.sha1(b'blob ' + str(len(raw)).encode() + b'\0' + raw).hexdigest() == oid,
                'worktree_blob_changed')
        records[name] = {'mode': mode, 'type': kind, 'oid': oid, 'sha256': hashlib.sha256(raw).hexdigest()}
    require(set(records) == set(anchor['baselineTrackedFiles']) | set(OWNED_FILES), 'source_file_set_changed')
    require({k:v for k,v in records.items() if k not in OWNED_FILES} == anchor['baselineTrackedFiles'],
            'product_projection_changed')
    require(all(records[name]['mode'] == '100644' for name in OWNED_FILES), 'ci_mode_changed')
    require(not git('status', '--porcelain=v1', '--untracked-files=all'), 'source_not_clean')
    return records


def verify_event(values, event):
    required = {'GITHUB_ACTIONS':'true', 'GITHUB_EVENT_NAME':'create', 'GITHUB_REPOSITORY':REPOSITORY,
                'GITHUB_REF':'refs/heads/' + BRANCH, 'GITHUB_RUN_ATTEMPT':'1',
                'GITHUB_WORKSPACE':str(ROOT), 'RUNNER_ENVIRONMENT':'github-hosted',
                'RUNNER_OS':'Linux', 'RUNNER_ARCH':'X64', 'ImageOS':'ubuntu24',
                'ImageVersion':'20261004.327.1'}
    require(all(values.get(k) == v for k,v in required.items()), 'host_admission_failed')
    require(re.fullmatch('[0-9a-f]{40}', values.get('GITHUB_SHA','')) is not None
            and re.fullmatch('[1-9][0-9]{0,19}', values.get('GITHUB_RUN_ID','')) is not None,
            'event_identity_invalid')
    require(type(event) is dict and event.get('ref_type') == 'branch' and event.get('ref') == BRANCH,
            'create_event_mismatch')
    repo = event.get('repository')
    require(type(repo) is dict and repo.get('full_name') == REPOSITORY and repo.get('private') is False
            and repo.get('default_branch') == 'main', 'repository_event_mismatch')


def output_flag(name):
    require(name in ('admitted', 'export_ready'), 'invalid_output_flag')
    path = os.environ.get('GITHUB_OUTPUT','')
    require(path.startswith('/home/runner/work/_temp/_runner_file_commands/set_output_')
            and pathlib.Path(path).is_absolute(), 'github_output_path_invalid')
    with open(path, 'a', encoding='utf-8') as stream:
        stream.write(name + '=true\n')


def admission():
    require(sys.flags.isolated and sys.flags.no_site and sys.flags.dont_write_bytecode,
            'python_isolation_missing')
    require(os.uname().sysname == 'Linux' and os.uname().machine == 'x86_64'
            and hasattr(os, 'WNOWAIT') and hasattr(os, 'waitid'), 'platform_not_supported')
    require(pathlib.Path.cwd() == ROOT and ROOT.resolve() == ROOT, 'checkout_path_changed')
    event_path = os.environ.get('GITHUB_EVENT_PATH','')
    require(event_path == '/home/runner/work/_temp/_github_workflow/event.json', 'event_path_changed')
    verify_event(os.environ, strict_json(regular_bytes(event_path, 1 << 20)))
    anchor = load_anchor()
    source = source_inventory(anchor)
    commit, tree = git('rev-parse','HEAD'), git('rev-parse','HEAD^{tree}')
    require(commit == os.environ['GITHUB_SHA'] and git('show','-s','--format=%P','HEAD') == PUBLIC_PARENT
            and git('rev-parse',PUBLIC_PARENT + '^{tree}') == PUBLIC_PARENT_TREE, 'public_ancestry_mismatch')
    require(not os.path.lexists(OUT) and not os.path.lexists(BASE), 'scope_already_consumed')
    os.umask(0o077)
    OUT.mkdir(mode=0o700)
    for name in ['home','tmp','bin','gocache','gomodcache','gopath','evidence','generated']:
        (OUT/name).mkdir(mode=0o700)
    identity = OUT.stat()
    data = {'sourceCommit':commit, 'sourceTree':tree, 'sourceRecords':source,
            'sourceManifestSHA256':hashlib.sha256(encoded(source)).hexdigest(),
            'baselineProjectionSHA256':hashlib.sha256(encoded(anchor['baselineTrackedFiles'])).hexdigest(),
            'rootIdentity':[identity.st_dev,identity.st_ino],
            'pythonSHA256':sha('/usr/bin/python3'), 'gitSHA256':sha(GIT),
            'hostToolHashes':hosted_tool_observations(),
            'pythonVersion':'.'.join(map(str,sys.version_info[:3])),
            'gitVersion':git('--version').removeprefix('git version ')}
    save_new(OUT/'admission.json', data, 1 << 20)
    output_flag('admitted')
    print('phase=admitted')


def reload_admission():
    data = strict_json(regular_bytes(OUT/'admission.json', 1 << 20))
    facts = OUT.lstat()
    require(stat.S_ISDIR(facts.st_mode) and stat.S_IMODE(facts.st_mode) == 0o700
            and facts.st_uid == os.geteuid() and [facts.st_dev,facts.st_ino] == data['rootIdentity'],
            'setup_root_replaced')
    require(set(data)=={'sourceCommit','sourceTree','sourceRecords','sourceManifestSHA256',
            'baselineProjectionSHA256','rootIdentity','pythonSHA256','gitSHA256','hostToolHashes',
            'pythonVersion','gitVersion'},'admission_record_shape')
    require(data['sourceCommit'] == os.environ.get('GITHUB_SHA') and git('rev-parse','HEAD') == data['sourceCommit']
            and git('rev-parse','HEAD^{tree}') == data['sourceTree'], 'admitted_source_changed')
    anchor=load_anchor()
    require(source_inventory(anchor) == data['sourceRecords'], 'source_projection_drift')
    require(hashlib.sha256(encoded(data['sourceRecords'])).hexdigest()==data['sourceManifestSHA256']
            and hashlib.sha256(encoded(anchor['baselineTrackedFiles'])).hexdigest()==data['baselineProjectionSHA256'],
            'admission_projection_digest_changed')
    return data


def materialized_path(name):
    safe_relative(name)
    prefix, relative = name.split('/', 1)
    roots = {'source':ROOT, 'toolchain':GOROOT, 'modules':OUT/'gomodcache',
             'adapted':ROOT/'.sobalink-deps', 'generated':OUT/'generated'}
    require(prefix in roots, 'unapproved_input_role')
    return roots[prefix] / relative


def hash_inventory(expected):
    result = {}
    for name in sorted(expected):
        raw = regular_bytes(materialized_path(name), 256 << 20)
        result[name] = hashlib.sha256(raw).hexdigest()
    require(result == expected, 'pinned_input_drift')
    return result


def exact_toolchain(anchor):
    require(GOROOT.resolve() == GOROOT and stat.S_ISDIR(GOROOT.lstat().st_mode), 'toolchain_root_changed')
    found = set()
    for parent, dirs, files in os.walk(GOROOT, followlinks=False):
        for name in dirs + files:
            path = pathlib.Path(parent)/name
            facts = path.lstat()
            require(stat.S_ISREG(facts.st_mode) or stat.S_ISDIR(facts.st_mode), 'toolchain_special_file')
            if stat.S_ISREG(facts.st_mode):
                found.add('toolchain/' + path.relative_to(GOROOT).as_posix())
    require(found == set(anchor['toolchainFiles']), 'toolchain_file_set_changed')
    return hash_inventory(anchor['toolchainFiles'])


def verify_assets(anchor):
    names = sorted(k for k in anchor['baselineTrackedFiles'] if k.startswith('web/dist/'))
    require(len(names) == 7, 'asset_count_changed')
    h = hashlib.sha256(b'sobalink-p1-assets-v1\x00' + struct.pack('>H',len(names)))
    for name in names:
        relative = name.removeprefix('web/dist/').encode()
        raw = regular_bytes(ROOT/name, 2 << 20)
        digest = hashlib.sha256(raw).digest()
        require(digest.hex() == anchor['baselineTrackedFiles'][name]['sha256'], 'asset_bytes_changed')
        h.update(struct.pack('>H',len(relative)) + relative + struct.pack('>Q',len(raw)) + digest)
    require(h.hexdigest() == anchor['assetAggregateSHA256'], 'asset_aggregate_changed')

# Fixed setup/build allocation accounting. Roots are disjoint: cache is not
# counted again in build scratch, and adapted inputs are outside the checkout
# projection. Native scratch has its original separate 64 MiB accounting.
allocation_identities = {}
filesystem_alias = None


def logical_size(root, maximum, entries, allow_alias=False):
    global filesystem_alias
    if not os.path.lexists(root):
        return 0, 0
    facts = root.lstat()
    require(stat.S_ISDIR(facts.st_mode), 'allocation_root_invalid')
    identity = (facts.st_dev,facts.st_ino)
    require(allocation_identities.get(root,identity) == identity, 'allocation_root_replaced')
    allocation_identities[root] = identity
    total = count = 0
    def vanished(exc):
        require(isinstance(exc,FileNotFoundError) and exc.errno == errno.ENOENT
                and pathlib.Path(exc.filename).is_relative_to(root), 'allocation_walk_failed')
    for parent, dirs, files in os.walk(root,followlinks=False,onerror=vanished):
        for name in dirs+files:
            path = pathlib.Path(parent)/name
            count += 1
            require(count <= entries, 'allocation_entry_limit')
            try:
                item = path.lstat()
                if stat.S_ISLNK(item.st_mode) and allow_alias:
                    relative = path.relative_to(root).as_posix()
                    require(re.fullmatch(r'TestProcessEntryOwnedDirectoryNoFollowNativeFilesystem[0-9]{1,10}/001/alias',relative)
                            and item.st_uid == os.geteuid() and os.readlink(path) == 'child', 'unexpected_leaf_link')
                    require(filesystem_alias in (None,path), 'second_leaf_link')
                    filesystem_alias = path
                    for child in [path.parent,path.parent/'child']:
                        s = child.lstat()
                        require(stat.S_ISDIR(s.st_mode) and stat.S_IMODE(s.st_mode)==0o700
                                and s.st_uid==os.geteuid(), 'leaf_link_parent_invalid')
                elif stat.S_ISREG(item.st_mode):
                    total += item.st_size
                else:
                    require(stat.S_ISDIR(item.st_mode), 'allocation_special_file')
            except FileNotFoundError as exc:
                require(exc.errno == errno.ENOENT, 'allocation_stat_failed')
            require(total <= maximum, 'allocation_byte_limit')
    current = root.lstat()
    require((current.st_dev,current.st_ino)==identity,'allocation_root_replaced')
    return total,count


def sample_materialization(leaf=None):
    total_entries = 0
    setup_bytes = 0
    for path in [GOROOT,OUT/'gomodcache',OUT/'gopath',ROOT/'.sobalink-deps']:
        size,count = logical_size(path,8<<30,262144)
        setup_bytes += size
        total_entries += count
    require(setup_bytes <= 8<<30,'setup_materialization_limit')
    size,count=logical_size(OUT/'gocache',4<<30,262144)
    total_entries += count
    scratch_bytes_total=0
    for name in ['home','tmp','bin','evidence','generated']:
        size,count=logical_size(OUT/name,1<<30,262144,allow_alias=(leaf=='filesystem2' and name=='tmp'))
        scratch_bytes_total += size
        total_entries += count
    # All root-level state, manifests and maps are part of the same 1 GiB cap.
    for path in OUT.iterdir():
        if stat.S_ISREG(path.lstat().st_mode):
            scratch_bytes_total += path.stat().st_size
            total_entries += 1
    require(scratch_bytes_total <= 1<<30 and total_entries <= 262144,'build_allocation_limit')
    if leaf is not None:
        total,_=logical_size(OUT/'tmp',32<<20,4096,allow_alias=leaf=='filesystem2')
        phase_dir=OUT/'evidence'/leaf
        phase_size,_=logical_size(phase_dir,32<<20,4096)
        require(total+phase_size <= 64<<20,'leaf_allocation_limit')


def initialize_capture(seconds, name):
    global SPEC, receipt, lock, state, proc, readers, leader_unreaped, cleanup_started
    global phase, command_started, command_deadline
    SPEC = {'rawOutputBytes':8<<20, 'processReapMarginSeconds':5, 'readerAndGroupJoinMarginSeconds':5}
    receipt = {'id':name,'failureCategories':[],'firstFailure':None,'invocationAttempted':False,
               'processJoined':False,'readersJoined':False,'ownedGroupAbsent':False,
               'groupSignalAttempted':False,'groupSignals':0,'returncode':None,
               'leaderExitObservedBeforeOuterDeadline':False}
    lock=threading.RLock()
    state={'written':0,'seen':0,'captureFailure':None,'lateOutput':False,'lastOutputObservedAt':0.0}
    proc,readers,leader_unreaped,cleanup_started=None,[],False,None
    phase=name
    command_started=time.monotonic()
    command_deadline=command_started+seconds


def captured_command(name, argv, env, seconds, sampler):
    """Finite caller-owned phase; preserved WNOWAIT/group/capture rules."""
    global proc,readers,leader_unreaped,cleanup_started,command_started,command_deadline
    directory=OUT/'evidence'/name
    directory.mkdir(mode=0o700)
    initialize_capture(seconds,name)
    streams=[directory/'stdout',directory/'stderr']
    writer=save_native_receipt if name=='p1-native' else save_new
    writer(directory/'started.json',{'id':name,'argv':argv},65536)
    try:
        # One anchor immediately before Popen, never reset by a first failure.
        command_started=time.monotonic()
        command_deadline=command_started+seconds
        receipt['invocationAttempted']=True
        proc=subprocess.Popen(argv,cwd=ROOT,env=env,stdin=subprocess.DEVNULL,
                              stdout=subprocess.PIPE,stderr=subprocess.PIPE,bufsize=0,
                              close_fds=True,start_new_session=True)
        leader_unreaped=True
        for pipe,destination in zip([proc.stdout,proc.stderr],streams):
            readers.append({'thread':threading.Thread(target=capture,args=(pipe,destination),daemon=True),
                            'pipe':pipe,'started':False})
        for item in readers:
            item['thread'].start()
            item['started']=True
        while True:
            if receipt['failureCategories']:
                break
            if time.monotonic()>=command_deadline:
                fail('outer_command_timeout')
                break
            sampler()
            if time.monotonic()>=command_deadline:
                fail('outer_command_timeout')
                break
            if os.waitid(os.P_PID,proc.pid,os.WEXITED|os.WNOHANG|os.WNOWAIT) is not None:
                receipt['leaderExitObservedBeforeOuterDeadline']=True
                break
            time.sleep(0.05)
        cleanup_started=time.monotonic()
        sweep_owned_group()
        join_owned()
        require(receipt['returncode']==0,'supervisor_nonzero_exit')
        require(receipt['processJoined'] and receipt['ownedGroupAbsent'] and receipt['readersJoined'],
                'supervisor_cleanup_unjoined')
        require(not state['lateOutput'] and state['captureFailure'] is None,'capture_or_output_timing_failed')
        require(receipt['leaderExitObservedBeforeOuterDeadline'],'leader_exit_was_not_timely')
    except BaseException:
        fail('phase_command_failed')
    finally:
        if proc is not None and leader_unreaped:
            if cleanup_started is None:
                cleanup_started=time.monotonic()
            try:
                sweep_owned_group()
                join_owned()
            except BaseException:
                fail('native_cleanup_unproven')
        for item in readers:
            if not item['started']:
                try:
                    item['pipe'].close()
                except BaseException:
                    fail('unstarted_reader_pipe_close_failed')
        if proc is not None and (not receipt['processJoined'] or not receipt['ownedGroupAbsent']
                or not receipt['readersJoined'] or any(item['thread'].is_alive() for item in readers)):
            fail('native_cleanup_unjoined')
        receipt['elapsedSeconds']=round(time.monotonic()-command_started,6)
        if receipt['elapsedSeconds']>seconds+10:
            fail('outer_and_join_window_exceeded')
        receipt['rawBytes']=state['written']
        receipt['lateOutputObserved']=state['lateOutput']
        writer(directory/'receipt.json',receipt,1<<20)
    return dict(receipt),streams


def require_command(result):
    require(not result['failureCategories'] and result['returncode']==0 and result['processJoined']
            and result['ownedGroupAbsent'] and result['readersJoined'], 'command_not_successful')


def phase_command(name,argv,env,seconds,leaf=None):
    # All call sites below use reviewed constant commands or seven fixed stamps.
    require(name in set(COMMANDS)|{c['id'] for c in LEAF_COMMANDS},'unknown_phase')
    result,streams=captured_command(name,argv,env,seconds,lambda:sample_materialization(leaf))
    require_command(result)
    return result,streams


def metadata_inventory(path,anchor):
    raw=regular_bytes(path,8<<20).decode('utf-8')
    decoder=json.JSONDecoder(object_pairs_hook=no_duplicate_object)
    offset=0
    packages=[]
    while offset<len(raw):
        while offset<len(raw) and raw[offset].isspace():
            offset+=1
        if offset==len(raw): break
        package,offset=decoder.raw_decode(raw,offset)
        require(type(package) is dict and not package.get('Error') and not package.get('DepsErrors'),
                'metadata_package_error')
        packages.append(package)
    active={}
    generated={}
    fields=['GoFiles','CgoFiles','CFiles','CXXFiles','MFiles','HFiles','FFiles','SFiles',
            'SwigFiles','SwigCXXFiles','SysoFiles','EmbedFiles']
    generated_packages={
        'github.com/webkaz-labs/sobalink/cmd/soba.test':('generated/cmd/soba.test.go',285),
        'github.com/webkaz-labs/sobalink/internal/resourceacceptance.test':('generated/internal/resourceacceptance.test.go',46),
        'github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel.test':('generated/internal/resourceacceptance/processmodel.test.go',37)}
    for package in packages:
        require(type(package.get('ImportPath')) is str,'metadata_import_missing')
        for field in fields:
            for name in package.get(field,[]):
                path=pathlib.Path(name)
                if not path.is_absolute(): path=pathlib.Path(package['Dir'])/path
                content=regular_bytes(path,8<<20)
                is_generated=package.get('Name')=='main' and package['ImportPath'].endswith('.test') and field=='GoFiles'
                if is_generated:
                    require(package['ImportPath'] in generated_packages and path.is_relative_to(OUT/'gocache'),
                            'generated_main_path_changed')
                    normalized,count=generated_packages[package['ImportPath']]
                    require(normalized not in generated and len(re.findall(rb'\{"(Test[^"\n]+)",',content))==count
                            and b'TestMain(' not in content,'generated_main_registry_changed')
                    generated[normalized]=hashlib.sha256(content).hexdigest()
                    dest=materialized_path(normalized)
                    dest.parent.mkdir(mode=0o700,parents=True,exist_ok=True)
                    save_new(dest,content,1<<20)
                else:
                    normalized=None
                    for root,prefix in [(ROOT/'.sobalink-deps','adapted'),(ROOT,'source'),
                                        (GOROOT,'toolchain'),(OUT/'gomodcache','modules')]:
                        if path.is_relative_to(root):
                            normalized=prefix+'/'+path.relative_to(root).as_posix()
                            break
                    require(normalized is not None,'metadata_unknown_root')
                safe_relative(normalized)
                entry=active.setdefault(normalized,{'bytes':len(content),'sha256':hashlib.sha256(content).hexdigest(),
                                                   'fields':[],'packages':[]})
                require(entry['sha256']==hashlib.sha256(content).hexdigest(),'metadata_conflicting_path')
                if field not in entry['fields']: entry['fields'].append(field)
                if package['ImportPath'] not in entry['packages']: entry['packages'].append(package['ImportPath'])
    for entry in active.values():
        entry['fields'].sort();entry['packages'].sort()
    require(active==anchor['activeInputs'] and generated==anchor['generatedTestMainFiles'],
            'active_input_equivalence_failed')
    return active,generated


def validate_leaf_output(raw, expected):
    lines=raw.decode('utf-8').splitlines()
    runs=[]; passes=[]
    for index,line in enumerate(lines):
        if line.startswith('=== RUN   '): runs.append(line.removeprefix('=== RUN   '))
        elif line.startswith('--- PASS: '):
            match=re.fullmatch(r'--- PASS: (Test[A-Za-z0-9_]+) \([0-9]+(?:\.[0-9]+)?s\)',line)
            require(match is not None,'leaf_pass_shape')
            passes.append(match.group(1))
        else:
            require(line=='PASS' and index==len(lines)-1,'leaf_extra_output')
    require(lines and lines[-1]=='PASS' and len(lines)==2*len(expected)+1
            and runs==expected and passes==expected,'leaf_exact_run_pass_failed')
    return len(runs)



def git_commit_time_utc(timestamp):
    """Normalize the authenticated Git committer instant, never its wall time."""
    require(type(timestamp) is str and re.fullmatch(
        r'[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}'
        r'[+-](?:[01][0-9]|2[0-3]):[0-5][0-9]', timestamp) is not None,
        'commit_timestamp_invalid')
    try:
        instant=datetime.datetime.fromisoformat(timestamp)
        require(instant.tzinfo is not None and instant.utcoffset() is not None
                and instant.isoformat(timespec='seconds')==timestamp, 'commit_timestamp_invalid')
        return instant.astimezone(datetime.timezone.utc)
    except (ValueError,OverflowError):
        raise RuntimeError('commit_timestamp_invalid') from None


def inspect_images(path, admission_data):
    sections={};current=None
    for line in regular_bytes(path,8<<20).decode('utf-8').splitlines():
        if line=='\t':
            require(current is not None,'buildinfo_separator_invalid');continue
        if line and not line.startswith('\t'):
            name,version=line.rsplit(': ',1)
            role=pathlib.Path(name).name
            require(name==str(OUT/'bin'/role) and role in IMAGE_ROLES and role not in sections
                    and version=='go1.27.1','buildinfo_image_invalid')
            current={'version':version,'records':[],'settings':{}}
            sections[role]=current
        else:
            parts=line.split('\t')
            require(current is not None and len(parts)>=3 and parts[1] in ['path','mod','dep','=>','build'],
                    'buildinfo_record_invalid')
            if parts[1]=='build':
                require(len(parts)==3 and '=' in parts[2],'buildinfo_setting_invalid')
                key,value=parts[2].split('=',1)
                require(key not in current['settings'],'buildinfo_duplicate_setting')
                current['settings'][key]=value
            else:
                current['records'].append(parts[1:])
    require(set(sections)==set(IMAGE_ROLES),'buildinfo_missing_image')
    timestamp=git('show','-s','--format=%cI','HEAD')
    instant=git_commit_time_utc(timestamp)
    vcs_time=instant.strftime('%Y-%m-%dT%H:%M:%SZ')
    pseudo='v0.0.0-'+instant.strftime('%Y%m%d%H%M%S')+'-'+admission_data['sourceCommit'][:12]
    settings={'-buildmode':'exe','-compiler':'gc','-tags':TAGS,'-trimpath':'true','CGO_ENABLED':'0',
              'GOARCH':'amd64','GOOS':'linux','GOAMD64':'v1','vcs':'git',
              'vcs.revision':admission_data['sourceCommit'],'vcs.modified':'false','vcs.time':vcs_time}
    for role,value in sections.items():
        expected=[list(row) for row in BUILD_MODULE_RECORDS[role]]
        for row in expected:
            if row[0]=='mod':row[2]=pseudo
        require(value['records']==expected,'buildinfo_module_equivalence_failed')
        observed=dict(value['settings'])
        if '-pgo' in observed:
            require(observed.pop('-pgo')=='off','buildinfo_pgo_changed')
        require(observed==settings,'buildinfo_settings_changed')
    return sections


def build_configuration(data,anchor):
    command=list(COMMANDS['build-product']['argv'])
    command.remove('<EXACT_SEVEN_STAMP_LDFLAGS>')
    first={'processEntrySourceCommit':data['sourceCommit'], 'processEntrySourceTree':data['sourceTree'],
           'processEntrySourceManifestSHA256':data['sourceManifestSHA256'],
           'processEntryDependencyManifestSHA256':DEPENDENCY_SHA256,
           'processEntryToolchainSHA256':TOOLCHAIN_SHA256,'processEntryAssetSHA256':anchor['assetAggregateSHA256']}
    order=['processEntrySourceCommit','processEntrySourceTree','processEntrySourceManifestSHA256',
           'processEntryDependencyManifestSHA256','processEntryToolchainSHA256','processEntryAssetSHA256',
           'processEntryBuildConfigurationSHA256']
    configuration={'schema':1,'sourceCommit':data['sourceCommit'],'sourceTree':data['sourceTree'],
                   'cwd':str(ROOT),'commandBeforeLinkerInjection':command,'environment':OFFLINE_ENV,
                   'goBinary':GO,'goBinarySHA256':anchor['toolchainFiles']['toolchain/bin/go'],
                   'linkerOrder':order,'firstSixLinkerStamps':first,'target':'linux-amd64',
                   'seventhStampAlgorithm':'sha256_canonical_json_sorted_indent2_newline',
                   'linkerPackage':'github.com/webkaz-labs/sobalink/internal/resourceacceptance',
                   'noOtherLinkerFlags':True}
    raw=encoded(configuration)
    digest=hashlib.sha256(raw).hexdigest()
    save_new(OUT/'build-configuration.json',raw,1<<20)
    stamps=dict(first,processEntryBuildConfigurationSHA256=digest)
    tokens=[]
    for key in order:
        tokens.extend(['-X','github.com/webkaz-labs/sobalink/internal/resourceacceptance.'+key+'='+stamps[key]])
    command.insert(command.index('-o'),'-ldflags='+' '.join(tokens))
    return command,digest


def approved_inventories(data,anchor):
    return {'source':{'source/'+k:v['sha256'] for k,v in data['sourceRecords'].items()},
            'toolchain':anchor['toolchainFiles'],'dependencies':anchor['dependencyFiles'],
            'generatedMain':anchor['generatedTestMainFiles']}


def check_inputs(approved):
    return {role:hash_inventory(values) for role,values in approved.items()}



def git_control_inventory():
    directory=ROOT/'.git'
    require(directory.resolve()==directory and stat.S_ISDIR(directory.lstat().st_mode),'git_directory_changed')
    found={}
    paths=list(directory.iterdir())
    for sub in ['refs','info']:
        root=directory/sub
        if root.exists():
            require(stat.S_ISDIR(root.lstat().st_mode),'git_control_linked')
            for parent,dirs,files in os.walk(root,followlinks=False):
                for name in dirs:
                    require(stat.S_ISDIR((pathlib.Path(parent)/name).lstat().st_mode),'git_control_linked')
                paths.extend(pathlib.Path(parent)/name for name in files)
    for path in paths:
        facts=path.lstat()
        require(not stat.S_ISLNK(facts.st_mode),'git_control_linked')
        if stat.S_ISDIR(facts.st_mode):continue
        name=path.relative_to(directory).as_posix();safe_relative(name)
        found[name]=hashlib.sha256(regular_bytes(path,8<<20)).hexdigest()
    require(len(found)<=128 and 'HEAD' in found and 'config' in found and 'index' in found,'git_control_shape')
    return found


def runtime_inventory(approved,binaries):
    expected={str(materialized_path(name)):digest for values in approved.values() for name,digest in values.items()}
    for role,digest in binaries.items(): expected[str(OUT/'bin'/role)]=digest
    for name in ['admission.json','build-configuration.json','native-manifest.json','active-inputs.json',
                 'toolchain-files.json','dependency-files.json','build-info.private.json','build-inputs-before.json',
                 'build-inputs-after.json','prerequisites.json','acquisition-complete.json',
                 'git-control-before.json','git-control-after.json']:
        path=OUT/name
        expected[str(path)]=sha(path)
    # Fixed hosted Git metadata replaces the nonportable local control input.
    for name,digest in git_control_inventory().items():
        expected[str(ROOT/'.git'/name)]=digest
    return expected


def rehash_runtime(expected):
    return {name:hashlib.sha256(regular_bytes(pathlib.Path(name),256<<20)).hexdigest() for name in sorted(expected)}


native_receipt_bytes=0


def save_native_receipt(path,value,maximum=1<<20):
    global native_receipt_bytes
    raw=value if type(value) is bytes else encoded(value)
    require(native_receipt_bytes+len(raw)<=1<<20,'native_receipt_reserve_exceeded')
    save_new(path,raw,maximum)
    native_receipt_bytes+=len(raw)


def native_attempt(data,anchor,approved,binaries):
    global SPEC,manifest,scratch_root_identity,fixture_root,fixture_root_absent
    global private_identities,observed_socket_paths,sample_disappearances,native_receipt_bytes
    native_receipt_bytes=0
    require(not os.path.lexists(BASE) and not os.path.lexists(OUT/'native-manifest.json'),'native_already_attempted')
    save_native_receipt(OUT/'native-started.json',{'attempt':1,'test':TEST},16384)
    manifest={'assetSHA256':anchor['assetAggregateSHA256'],'binary':str(OUT/'bin/soba'),
              'binarySHA256':binaries['soba'],'cgoEnabled':False,'dependencyManifestSHA256':DEPENDENCY_SHA256,
              'scenario':1,'schemaVersion':1,'selector':'^'+TEST+'$','sourceCommit':data['sourceCommit'],
              'sourceManifestSHA256':data['sourceManifestSHA256'],'sourceTree':data['sourceTree'],
              'tags':TAGS,'target':'linux-amd64','toolchainSHA256':TOOLCHAIN_SHA256}
    raw=encoded(manifest)
    require(len(manifest)==14 and len(raw)<=16384,'native_manifest_shape')
    save_native_receipt(OUT/'native-manifest.json',raw,16384)
    manifest_hash=hashlib.sha256(raw).hexdigest()
    for path in [BASE,BASE/'home',BASE/'tmp']:path.mkdir(mode=0o700)
    facts=BASE.lstat()
    scratch_root_identity=(facts.st_dev,facts.st_ino)
    fixture_root,fixture_root_absent=None,False
    private_identities,observed_socket_paths={},set()
    sample_disappearances=0
    native_spec={'scratchEntries':16384,'socketException':{'relativePaths':['state/c/control.sock','state/a/control.sock','state/b/control.sock']},
                 'source':{'commit':data['sourceCommit'],'tree':data['sourceTree']},
                 'receiptRules':{'exactTest':TEST,'allUnjoinedZero':UNJOINED}}
    SPEC.update(native_spec)
    require(scratch_bytes(BASE)==0,'initial_native_scratch_not_empty')
    expected=runtime_inventory(approved,binaries)
    before=rehash_runtime(expected)
    require(before==expected and 2*len(encoded(before))<=12<<20,'native_input_reserve_failed')
    save_new(OUT/'native-inputs-before.json',before,6<<20)
    def sample_native():
        SPEC.update(native_spec)
        require(scratch_bytes(BASE)<=32<<20,'scratch_overflow')
    # Capture/group helpers and native sampler retain the original 360+5+5 policy.
    result,streams=captured_command('p1-native',COMMANDS['p1-native']['argv'],NATIVE_ENV,360,sample_native)
    SPEC.update(native_spec)
    result.update({'fixtureReceipt':None,'inputsUnchanged':False,'sourceIdentityUnchanged':False,
                   'fixtureRootTerminallyAbsent':False,'allChecksPassed':False})
    try:
        after=rehash_runtime(expected)
        save_new(OUT/'native-inputs-after.json',after,6<<20)
        result['inputsUnchanged']=before==after
        result['sourceIdentityUnchanged']=source_inventory(anchor)==data['sourceRecords']
        require(result['inputsUnchanged'] and result['sourceIdentityUnchanged'],'native_input_drift')
        facts=(OUT/'native-manifest.json').lstat()
        require(stat.S_ISREG(facts.st_mode) and stat.S_IMODE(facts.st_mode)==0o600
                and facts.st_uid==os.geteuid() and facts.st_nlink==1
                and regular_bytes(OUT/'native-manifest.json',16384)==raw,'native_manifest_changed')
        require_command(result)
        require(streams[1].stat().st_size==0,'unexpected_test_stderr')
        result['fixtureReceipt']=validate_fixture_stdout(streams[0])
        require(scratch_bytes(BASE)==0 and fixture_root is not None and fixture_root_absent
                and not os.path.lexists(fixture_root),'successful_fixture_root_not_removed')
        result['fixtureRootTerminallyAbsent']=True
        result['allChecksPassed']=True
    except BaseException:
        result['failureCategories'].append('native_validation_failed')
    result['rawOutputSeenBytes']=state['seen']
    result['rawOutputRetainedBytes']=state['written']
    result['commandAndJoinElapsedMicros']=int(result['elapsedSeconds']*1000000)
    save_native_receipt(OUT/'native-result.private.json',result,1<<20)
    return result,manifest_hash


def run_hosted():
    data=reload_admission();anchor=load_anchor()
    os.umask(0o077)
    save_new(OUT/'run-started.json',{'attempt':1},16384)
    acquisition=strict_json(regular_bytes(OUT/'acquisition-complete.json',16384))
    require(set(acquisition)=={'archiveBytes','archiveSHA256','toolchainManifestSHA256','files','elapsedMicros'}
            and type(acquisition['archiveBytes']) is int and acquisition['archiveBytes']==GO_ARCHIVE_BYTES
            and acquisition['archiveSHA256']==GO_ARCHIVE_SHA256
            and acquisition['toolchainManifestSHA256']==TOOLCHAIN_SHA256
            and type(acquisition['files']) is int and acquisition['files']==15639
            and type(acquisition['elapsedMicros']) is int and 0<=acquisition['elapsedMicros']<=420000000,
            'acquisition_receipt_invalid')
    status={'phase':'harness-values','failureCategory':'prerequisite_failed','outcome':'FAILED',
            'before':None,'after':None,'buildInfo':None,'binaries':None,'native':None,
            'configHash':None,'manifestHash':None,'prerequisiteCounts':{},'toolchainVerified':False}
    try:
        # The authenticated official archive has already been installed. No
        # Go invocation precedes the independent full toolchain-file check.
        exact_toolchain(anchor);status['toolchainVerified']=True
        verify_assets(anchor)
        phase_command('harness-values',COMMANDS['harness-values']['argv'],OFFLINE_ENV,120)
        setup_started=time.monotonic()
        for name in ['prepare-engine','verify-engine','module-download','module-verify']:
            status['phase']=name;status['failureCategory']='setup_failed'
            command=COMMANDS[name]
            environment=SETUP_ENV if name in ('prepare-engine','module-download') else OFFLINE_ENV
            phase_command(name,command['argv'],environment,command['maxSeconds'])
            require(time.monotonic()-setup_started<=1080,'setup_aggregate_deadline')
            require(source_inventory(anchor)==data['sourceRecords'],'setup_source_drift')
        status['phase']='metadata';status['failureCategory']='input_drift'
        hash_inventory(anchor['dependencyFiles']);exact_toolchain(anchor)
        command=COMMANDS['metadata'];unused,streams=phase_command('metadata',command['argv'],OFFLINE_ENV,120)
        active,generated=metadata_inventory(streams[0],anchor)
        save_new(OUT/'active-inputs.json',active)
        save_new(OUT/'toolchain-files.json',anchor['toolchainFiles'])
        save_new(OUT/'dependency-files.json',anchor['dependencyFiles'])
        approved=approved_inventories(data,anchor)
        status['before']=check_inputs(approved)
        save_new(OUT/'build-inputs-before.json',status['before'])
        git_before=git_control_inventory()
        save_new(OUT/'git-control-before.json',git_before,1<<20)
        product,config_hash=build_configuration(data,anchor);status['configHash']=config_hash
        status['phase']='build-product';status['failureCategory']='build_failed'
        phase_command('build-product',product,OFFLINE_ENV,420)
        status['phase']='build-tests'
        phase_command('build-tests',COMMANDS['build-tests']['argv'],OFFLINE_ENV,480)
        binaries={}
        for name in IMAGE_ROLES:
            path=OUT/'bin'/name;facts=path.lstat()
            require(stat.S_ISREG(facts.st_mode) and facts.st_uid==os.geteuid()
                    and facts.st_mode&0o111 and 0<facts.st_size<=256<<20,'compiled_image_invalid')
            binaries[name]=sha(path)
        status['phase']='inspect-images'
        unused,streams=phase_command('inspect-images',COMMANDS['inspect-images']['argv'],OFFLINE_ENV,30)
        build_info=inspect_images(streams[0],data)
        save_new(OUT/'build-info.private.json',build_info)
        status['buildInfo']=build_info;status['binaries']=binaries
        status['after']=check_inputs(approved)
        save_new(OUT/'build-inputs-after.json',status['after'])
        require(source_inventory(anchor)==data['sourceRecords'],'build_source_drift')
        git_after=git_control_inventory()
        save_new(OUT/'git-control-after.json',git_after,1<<20)
        require(git_before==git_after,'build_git_control_drift')
        status['phase']='leaf-prerequisites';status['failureCategory']='prerequisite_failed'
        prereqs={}
        for command in LEAF_COMMANDS:
            require(not list((OUT/'tmp').iterdir()) and not list((OUT/'home').iterdir()),'leaf_scratch_not_empty')
            check_inputs(approved)
            require(all(sha(OUT/'bin'/key)==value for key,value in binaries.items()),'image_drift')
            result,streams=phase_command(command['id'],command['argv'],OFFLINE_ENV,130,leaf=command['id'])
            require(streams[1].stat().st_size==0,'leaf_stderr_nonempty')
            count=validate_leaf_output(regular_bytes(streams[0],8<<20),command['tests'])
            require(not list((OUT/'tmp').iterdir()) and not list((OUT/'home').iterdir()),'leaf_scratch_retained')
            check_inputs(approved)
            prereqs[command['id']]={'count':count,'receiptSHA256':sha(OUT/'evidence'/command['id']/'receipt.json')}
            status['prerequisiteCounts'][command['id']]=count
        require(sum(v['count'] for v in prereqs.values())==83,'prerequisite_count_invalid')
        save_new(OUT/'prerequisites.json',prereqs,65536)
        status['phase']='p1-native';status['failureCategory']='native_failed'
        status['after']=None
        result,digest=native_attempt(data,anchor,approved,binaries)
        status['native']=result;status['manifestHash']=digest
        status['after']=check_inputs(approved)
        require(result['allChecksPassed'],'native_failed')
        status['outcome']='PASSED';status['failureCategory']='none'
    except BaseException:
        # Raw exceptions and child output never enter Actions logs or artifacts.
        pass
    finally:
        save_new(OUT/'execution.private.json',status,16<<20)
        print('phase='+status['phase']+' outcome='+status['outcome'])
    return status['outcome']=='PASSED'


def hosted_tool_observations():
    # These are ordinary image observations, not independently pinned inputs.
    # No ldd, loader invocation, environment dump, or process discovery is used.
    libraries={}
    for line in pathlib.Path('/proc/self/maps').read_text(encoding='utf-8').splitlines():
        parts=line.split(maxsplit=5)
        if len(parts)!=6 or not parts[5].startswith('/'):continue
        path=pathlib.Path(parts[5])
        if path.name.endswith('.so') or '.so.' in path.name:
            resolved=path.resolve()
            require(resolved.is_relative_to('/usr/lib') or resolved.is_relative_to('/usr/local/lib'),
                    'host_library_path_changed')
            libraries[resolved.as_posix()]=sha(resolved)
    require(0<len(libraries)<=128,'host_library_observation_invalid')
    loader=pathlib.Path('/lib64/ld-linux-x86-64.so.2').resolve()
    require(loader.is_relative_to('/usr/lib/x86_64-linux-gnu'),'host_loader_path_changed')
    return {'loader':sha(loader),'libraryInventory':hashlib.sha256(encoded(libraries)).hexdigest(),
            'osRelease':sha(pathlib.Path('/etc/os-release').resolve()),
            'kernelRelease':hashlib.sha256(os.uname().release.encode()).hexdigest()}



def validate_native_terminal_for_export(value, attempt_marker):
    """Missing/falsey malformed terminal data never turns an attempt into NOT_RUN."""
    require(type(attempt_marker) is bool, 'native_marker_type')
    if value is None:
        require(not attempt_marker, 'native_attempt_without_terminal_evidence')
        return None
    require(attempt_marker and type(value) is dict, 'native_terminal_type')
    required=set(PUBLIC_OUTER_KEYS)|{'id','failureCategories','firstFailure','elapsedSeconds','rawBytes','fixtureReceipt'}
    require(set(value) in (required,required|{'retainedGroupAlreadyAbsent'}), 'native_terminal_shape')
    require(value['id']=='p1-native' and value['invocationAttempted'] is True, 'native_terminal_identity')
    _public_outer({key:value[key] for key in PUBLIC_OUTER_KEYS})
    categories=('output_observed_after_outer_deadline','output_overflow','capture_failed',
        'capture_pipe_close_failed','retained_group_signal_failed','supervisor_process_unjoined',
        'supervisor_reap_deadline_exceeded','supervisor_reap_unproven','supervisor_group_unjoined',
        'supervisor_readers_unjoined','cleanup_join_deadline_exceeded','outer_command_timeout',
        'phase_command_failed','native_cleanup_unproven','unstarted_reader_pipe_close_failed',
        'native_cleanup_unjoined','outer_and_join_window_exceeded','native_validation_failed')
    failures=value['failureCategories']
    require(type(failures) is list and len(failures)<=len(categories)
            and all(type(item) is str and item in categories for item in failures)
            and len(failures)==len(set(failures)), 'native_terminal_failures')
    first=value['firstFailure']
    if first is not None:
        require(type(first) is dict and set(first)=={'category','phase','elapsedSeconds'}
                and type(first['category']) is str and first['category'] in failures
                and first['phase']=='p1-native' and type(first['elapsedSeconds']) in (int,float)
                and 0<=first['elapsedSeconds']<=3600, 'native_terminal_first_failure')
    require(type(value['elapsedSeconds']) in (int,float) and 0<=value['elapsedSeconds']<=3600
            and type(value['rawBytes']) is int and value['rawBytes']==value['rawOutputRetainedBytes'],
            'native_terminal_counters')
    require(value['fixtureReceipt'] is None or type(value['fixtureReceipt']) is dict,
            'native_terminal_fixture_type')
    if 'retainedGroupAlreadyAbsent' in value:
        require(value['retainedGroupAlreadyAbsent'] is True and value['groupSignalAttempted'] is True
                and value['groupSignals']==0, 'native_terminal_absent_group')
    require(not value['allChecksPassed'] or (not failures and value['fixtureReceipt'] is not None),
            'native_terminal_success_conflict')
    return value


def validate_execution_state_for_export(value):
    require(type(value) is dict and set(value)=={'phase','failureCategory','outcome','before','after',
        'buildInfo','binaries','native','configHash','manifestHash','prerequisiteCounts','toolchainVerified'},
        'execution_state_shape')
    require(value['phase'] in PUBLIC_PHASES and value['failureCategory'] in PUBLIC_FAILURE_CATEGORIES
            and value['outcome'] in ('FAILED','PASSED') and type(value['toolchainVerified']) is bool,
            'execution_state_scalar')
    for key in ['before','after','buildInfo','binaries','native']:
        require(value[key] is None or type(value[key]) is dict, 'execution_state_nested_type')
    for key in ['configHash','manifestHash']:
        _public_hash(value[key],nullable=True)
    expected={'observer32':32,'model37':37,'pipes11':11,'signal1':1,'filesystem2':2}
    counts=value['prerequisiteCounts']
    require(type(counts) is dict and set(counts).issubset(expected)
            and all(type(count) is int and count==expected[key] for key,count in counts.items()),
            'execution_state_prerequisites')
    if value['binaries'] is not None:
        require(set(value['binaries'])==set(IMAGE_ROLES),'execution_state_binaries')
        for digest in value['binaries'].values():_public_hash(digest)
    return value


def export_hosted():
    data=reload_admission();anchor=load_anchor()
    os.umask(0o077)
    approved=approved_inventories(data,anchor)
    private=OUT/'execution.private.json'
    status=validate_execution_state_for_export(strict_json(regular_bytes(private,16<<20))) if private.exists() else None
    build={key:None for key in PUBLIC_IMAGE_ROLES}
    filename_roles=dict(zip(IMAGE_ROLES,PUBLIC_IMAGE_ROLES))
    if status is not None and status['buildInfo'] is not None:
        # Revalidate bounded raw go-version output; no unvalidated stored free text.
        inspected=inspect_images(OUT/'evidence/inspect-images/stdout',data)
        require(inspected==status['buildInfo'],'stored_build_info_changed')
        for filename,role in filename_roles.items():
            info=inspected[filename]
            require(sha(OUT/'bin'/filename)==status['binaries'][filename],'export_image_drift')
            build[role]={'goVersion':'go1.27.1','modulePath':'github.com/webkaz-labs/sobalink',
                'moduleVersion':next(row[2] for row in info['records'] if row[0]=='mod'),
                'packagePath':PUBLIC_PACKAGE_PATHS[role],'revision':data['sourceCommit'],
                'vcsModified':False,'goos':'linux','goarch':'amd64','goamd64':'v1','cgoEnabled':False,
                'trimpath':True,'buildvcs':True,'pgo':'off','tags':TAGS,'buildMode':'exe',
                'compiler':'gc','settingsVerified':True,'imageSHA256':status['binaries'][filename]}
    native=validate_native_terminal_for_export(status['native'] if status is not None else None,
        (OUT/'native-started.json').exists())
    outer={key:False for key in PUBLIC_OUTER_BOOL_KEYS}
    outer.update({'groupSignals':0,'leaderExitObservedBeforeOuterDeadline':None,'returncode':None,
                  'commandAndJoinElapsedMicros':None,'rawOutputSeenBytes':0,'rawOutputRetainedBytes':0})
    if native is not None:
        for key in PUBLIC_OUTER_KEYS:outer[key]=native[key]
    inner=None
    tests={'run':0,'pass':0,'fail':0,'notRun':int(not outer['invocationAttempted']),'syntaxValidated':False}
    bindings={'source':data['sourceCommit'],'tree':data['sourceTree'],
              'sourceManifest':data['sourceManifestSHA256'],
              'binary':None if build['product'] is None else build['product']['imageSHA256'],
              'assets':anchor['assetAggregateSHA256'],'dependencies':DEPENDENCY_SHA256,'toolchain':TOOLCHAIN_SHA256}
    if native is not None and all(outer[key] for key in ['processJoined','readersJoined','ownedGroupAbsent']):
        if native['allChecksPassed']:
            inner=native['fixtureReceipt'];tests.update(run=1,**{'pass':1,'syntaxValidated':True})
        else:
            try:
                raw=regular_bytes(OUT/'evidence/p1-native/stdout',32768)
                inner=validate_failure_stdout(raw,bindings)
                tests.update(run=1,fail=1,syntaxValidated=True)
            except (OSError,ValueError,RuntimeError):
                pass
    before={role:None for role in PUBLIC_INVENTORY_ROLES}
    after=dict(before)
    if status is not None and status['before'] is not None:before=status['before']
    if status is not None and status['after'] is not None:after=status['after']
    counts={} if status is None else status['prerequisiteCounts']
    expected_counts={'observer32':32,'model37':37,'pipes11':11,'signal1':1,'filesystem2':2}
    require(set(counts).issubset(expected_counts) and all(type(v) is int and v==expected_counts[k]
            for k,v in counts.items()),'stored_prerequisite_counts_invalid')
    count=sum(counts.values())
    observations=data['hostToolHashes']
    hashes={key:None for key in PUBLIC_HASH_KEYS}
    hashes.update(assets=anchor['assetAggregateSHA256'],dependencyManifest=DEPENDENCY_SHA256,
                  toolchainManifest=TOOLCHAIN_SHA256)
    if status is not None:
        hashes.update(buildConfiguration=status['configHash'],nativeManifest=status['manifestHash'])
    for name,key in [('native-inputs-before.json','runtimeInputsBefore'),('native-inputs-after.json','runtimeInputsAfter')]:
        path=OUT/name
        if path.exists():hashes[key]=hashlib.sha256(regular_bytes(path,6<<20)).hexdigest()
    context={'phase':'admit' if status is None else status['phase'],
             'outcome':'not_run' if not outer['invocationAttempted'] else ('passed' if native['allChecksPassed'] else 'failed'),
             'failureCategory':'setup_failed' if status is None else status['failureCategory'],
             'source':{'commit':data['sourceCommit'],'tree':data['sourceTree'],'parent':PUBLIC_PARENT,
                       'parentTree':PUBLIC_PARENT_TREE,'baselineProjectionSHA256':data['baselineProjectionSHA256'],
                       'sourceManifestSHA256':data['sourceManifestSHA256']},
             'hashes':hashes,'actionPins':PUBLIC_ACTION_PINS,
             'image':{'label':'ubuntu-24.04','ImageOS':'ubuntu24','ImageVersion':'20261004.327.1',
                      'runnerEnvironment':'github-hosted','architecture':'X64'},
             'toolHashes':dict(observations,python=data['pythonSHA256'],git=data['gitSHA256'],
                               go=anchor['toolchainFiles']['toolchain/bin/go'] if status is not None and status['toolchainVerified'] else None),
             'toolVersions':{'python':data['pythonVersion'],'git':data['gitVersion'],
                             'go':'go1.27.1' if status is not None and status['toolchainVerified'] else None},
             'buildInfo':build,'prerequisites':{'validationScope':'completed_successful_cohorts_only',
                                              'verifiedRun':count,'verifiedPass':count,
                                              'completedCohorts':len(counts),'unclassifiedOrNotRunCohorts':5-len(counts),
                                              'allChecksPassed':len(counts)==5 and count==83},
             'outer':outer,'testOutput':tests,'innerReceipt':inner,'before':before,'after':after}
    artifacts=construct_public_artifacts(context,approved)
    validate_public_artifacts(artifacts,approved)
    directory=OUT/'public'
    directory.mkdir(mode=0o700)
    for name in PUBLIC_ARTIFACT_NAMES:save_new(directory/name,artifacts[name],PUBLIC_EXPORT_LIMIT)
    # Final reparse includes actual bytes, exact file set and aggregate cap.
    require(set(path.name for path in directory.iterdir())==set(PUBLIC_ARTIFACT_NAMES),'export_file_set_changed')
    actual={name:regular_bytes(directory/name,PUBLIC_EXPORT_LIMIT) for name in PUBLIC_ARTIFACT_NAMES}
    validate_public_artifacts(actual,approved)
    output_flag('export_ready')
    print('phase=export outcome=ready')


def main():
    require(len(sys.argv)==2 and sys.argv[1] in ('admit','acquire','run','export'),'invalid_command')
    if sys.argv[1]=='admit':admission();return 0
    if sys.argv[1]=='acquire':acquire_official_go();return 0
    if sys.argv[1]=='run':return 0 if run_hosted() else 1
    export_hosted();return 0


# Pure export helpers. Integration requires only the standard json and re imports.
# No file, environment, clock, subprocess, or network access occurs here.
PUBLIC_EXPORT_LIMIT = 16 * 1024 * 1024
PUBLIC_TEST_NAME = 'TestResourceProcessControllerRestartKeepsHistoryNoReplay'
PUBLIC_ACTION_PINS = {
    'checkout': '3d3c42e5aac5ba805825da76410c181273ba90b1',
    'uploadArtifact': '043fb46d1a93c77aae656e7c1c64a875d1fc6a0a',
}
PUBLIC_GO_ACQUISITION_POLICY = {
    'scheme': 'official_go_archive_sha256_before_extract_v1',
    'archiveSHA256': '63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445',
    'archiveBytes': 70553950, 'goVersion': 'go1.27.1',
    'verificationOrder': 'archive_bytes_then_sha256_then_extract_then_complete_15639_file_map_before_go',
}
PUBLIC_ARTIFACT_NAMES = (
    'result.json', 'run-pass-summary.txt', 'provenance.json',
    'build-info.json', 'input-hashes.json', 'child-error-category.json',
)
PUBLIC_INVENTORY_ROLES = ('source', 'toolchain', 'dependencies', 'generatedMain')
PUBLIC_IMAGE_ROLES = ('product', 'sobaTest', 'observerTest', 'modelTest')
PUBLIC_PACKAGE_PATHS = {
    'product': 'github.com/webkaz-labs/sobalink/cmd/soba',
    'sobaTest': 'github.com/webkaz-labs/sobalink/cmd/soba.test',
    'observerTest': 'github.com/webkaz-labs/sobalink/internal/resourceacceptance.test',
    'modelTest': 'github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel.test',
}
PUBLIC_TAGS = ('resource_process_native,directlan_activation_native,ts_omit_portmapper,'
               'ts_omit_captiveportal,ts_omit_useproxy')
PUBLIC_PHASES = (
    'admit', 'harness-values', 'prepare-engine', 'verify-engine', 'module-download',
    'module-verify', 'metadata', 'build-product', 'build-tests', 'inspect-images',
    'leaf-prerequisites', 'p1-native', 'export',
)
PUBLIC_FAILURE_CATEGORIES = (
    'none', 'admission_failed', 'setup_failed', 'input_drift', 'build_failed',
    'prerequisite_failed', 'native_failed', 'native_timeout', 'cleanup_unproven',
    'receipt_invalid', 'unclassified_failure',
)
PUBLIC_UNJOINED_BOUNDS = {
    'start': 45, 'exit': 45, 'wait': 45, 'stdout': 45, 'stderr': 45,
    'writer': 45, 'handle': 512, 'driver': 1, 'reservation': 3,
}
PUBLIC_INTENDED_COUNTS = {
    'productOwnerIncarnations': 5, 'productCLIInvocations': 40,
    'checkpoints': 26, 'nodeAllocations': 9, 'supervisorInvocations': 1,
}
PUBLIC_CLAIM_LIMITS = {
    'scope': 'instrumented_source_built_linux_entry_only',
    'installedBinaryAcceptance': False, 'otherTargetAcceptance': False,
    'transferAcceptance': False, 'receiveState': 'legacy_review_required',
    'groupAbsenceProvesApplicationJoins': False, 'samplerIsKernelQuota': False,
    'hostedProvenanceIsIndependentByteAuthentication': False, 'oneAttempt': True,
}
PUBLIC_SOURCE_KEYS = (
    'commit', 'tree', 'parent', 'parentTree', 'baselineProjectionSHA256',
    'sourceManifestSHA256',
)
PUBLIC_HASH_KEYS = (
    'assets', 'dependencyManifest', 'toolchainManifest', 'buildConfiguration',
    'nativeManifest', 'runtimeInputsBefore', 'runtimeInputsAfter',
)
PUBLIC_OUTER_BOOL_KEYS = (
    'invocationAttempted', 'processJoined', 'readersJoined', 'ownedGroupAbsent',
    'groupSignalAttempted', 'inputsUnchanged', 'sourceIdentityUnchanged',
    'fixtureRootTerminallyAbsent', 'lateOutputObserved', 'allChecksPassed',
)
PUBLIC_OUTER_KEYS = PUBLIC_OUTER_BOOL_KEYS + (
    'groupSignals', 'leaderExitObservedBeforeOuterDeadline', 'returncode',
    'commandAndJoinElapsedMicros', 'rawOutputSeenBytes', 'rawOutputRetainedBytes',
)
PUBLIC_TOOL_HASH_KEYS = (
    'python', 'git', 'go', 'loader', 'libraryInventory', 'osRelease', 'kernelRelease',
)
PUBLIC_BUILD_KEYS = (
    'goVersion', 'modulePath', 'moduleVersion', 'packagePath', 'revision',
    'vcsModified', 'goos', 'goarch', 'goamd64', 'cgoEnabled', 'trimpath',
    'buildvcs', 'pgo', 'tags', 'buildMode', 'compiler', 'settingsVerified',
    'imageSHA256',
)

# Closed stage literals from the unchanged P1 fixture require/waitChanged sites.
PUBLIC_FAILURE_STAGES = (
    'accepted_input_encoding',
    'aggregate_quota',
    'apply_input',
    'argv_digest',
    'atomic_namespace_entry',
    'atomic_namespace_marker',
    'authority_without_run',
    'bilateral_pair',
    'binary_identity',
    'binary_open',
    'binary_replaced',
    'binary_size',
    'binary_stat',
    'bootstrap_digest',
    'bootstrap_encode',
    'bootstrap_evidence',
    'bootstrap_timeout',
    'bootstrap_write',
    'catalog_changed_inside_owner',
    'checkpoint_count',
    'checkpoint_encode',
    'checkpoint_group_order',
    'checkpoint_random',
    'checkpoint_timeout',
    'checkpoint_write',
    'child_exit_failed',
    'child_exit_unobserved',
    'child_handle_unjoined',
    'child_pipe_copies',
    'child_start',
    'child_summary_receipt',
    'child_transcript_unsealed',
    'child_wait_unjoined',
    'cleanup_original_cutoff',
    'cleanup_reservation',
    'cli_assertion_missing',
    'cli_evidence_failed',
    'cli_exit_error',
    'cli_finality',
    'cli_negotiated_dials',
    'cli_product_failure',
    'cli_schedule',
    'cli_unexpected_observation',
    'cli_wait_error',
    'command_ledger',
    'completed_group',
    'completed_member',
    'completed_original_request',
    'concurrent_web_origins',
    'constructor_fallback',
    'control_constructor_count',
    'control_join_before_ordinary',
    'control_namespace',
    'controller_dispatch_order',
    'controller_journal_count',
    'controller_original_journal',
    'controller_original_origin',
    'current_review_decode',
    'directory_create',
    'dispatch_enum',
    'driver_cleanup_unjoined',
    'driver_command',
    'driver_payload',
    'driver_reply',
    'driver_request_missing',
    'driver_serialization',
    'driver_unjoined',
    'dry_run_apply',
    'dry_run_dial',
    'dry_run_envelope',
    'dry_run_owner_events',
    'dry_run_preview',
    'dry_run_state_changed',
    'dry_run_status',
    'duplicate_management_event',
    'durable_grant',
    'endpoint_reservation',
    'entry_readiness_order',
    'evidence_directory',
    'evidence_open',
    'file_owner_capacity',
    'final_cli_join',
    'final_journal_decode',
    'final_owner_join',
    'final_schedule',
    'first_pair_changed',
    'fixture_random',
    'grant_actions',
    'grant_scope',
    'grant_stability',
    'group_acceptance',
    'history_changed',
    'human_english',
    'human_japanese',
    'human_private_material',
    'human_profile_guidance',
    'human_seed',
    'implicit_trust',
    'inherited_group',
    'initial_authority_present',
    'initial_journal',
    'initial_profile',
    'intent_run',
    'inventory_binding',
    'inventory_open',
    'join_owner',
    'lan_peer',
    'lan_star',
    'lan_state',
    'launch_random',
    'launch_selection',
    'lifecycle_identity',
    'literal_request_digest',
    'local_enum',
    'local_request_identity',
    'maintenance_timeout',
    'management_controller_owner',
    'management_replayed',
    'management_total',
    'manifest_digest',
    'manifest_field_missing',
    'manifest_fields',
    'manifest_read',
    'manifest_schema',
    'manifest_target',
    'measured_catalog_reused',
    'missing_verification',
    'negative_revision',
    'negative_typed_error',
    'negotiation_dispatch_ledger',
    'normal_stop_dispatch',
    'observer_cleanup_unjoined',
    'operation_observation',
    'operation_observation_id',
    'ordinary_constructor_count',
    'original_grant_changed',
    'original_grant_expired',
    'output_read',
    'output_relative',
    'owned_root_removal_failed',
    'owner_a_order',
    'owner_b_order',
    'owner_binding_count',
    'owner_c0_order',
    'owner_c1_predecessor',
    'owner_c2_predecessor',
    'owner_evidence_failed',
    'owner_final_events',
    'owner_outbound_private_ipc',
    'owner_product_exit',
    'owner_readiness_timeout',
    'owner_schedule',
    'owner_stderr',
    'owner_web_origin',
    'owner_web_output',
    'pair_authority_changed',
    'pair_binding',
    'pair_initial_endpoint',
    'pair_schedule',
    'persisted_identity_changed',
    'phase_budget_exhausted',
    'pipe_tasks_unjoined',
    'prepared_apply',
    'prepared_complete',
    'prepared_decode',
    'prepared_target',
    'preview_event_kind',
    'preview_owner',
    'private_read',
    'private_write',
    'private_write_open',
    'profile_changed',
    'provider_owner',
    'public_group_redaction',
    'request_ledger_capacity',
    'reservation_close',
    'resource_catalog',
    'resource_descriptor',
    'resource_identity_changed',
    'resource_inspect',
    'resource_inspect_settings',
    'response_bound',
    'role_handle_changed',
    'role_handle_identity',
    'role_open',
    'role_path_changed',
    'role_private',
    'role_replaced',
    'root_create',
    'root_handle_identity',
    'root_open',
    'root_private',
    'run_decode',
    'run_observation_id',
    'saved_service_count',
    'saved_service_semantics',
    'seed_authority',
    'seed_direct_config',
    'seed_encoding',
    'selection_decode',
    'selection_encode',
    'serial_controller_dispatch',
    'setup_catalog_reused',
    'sidecar_namespace_entry',
    'spawn_handle',
    'start_task_unjoined',
    'status_empty_intent',
    'status_field_missing',
    'status_fields',
    'status_network',
    'status_peers',
    'status_process',
    'status_settings',
    'status_unrequested_activity',
    'stderr_pipe',
    'stdin_pipe',
    'stdout_pipe',
    'strict_response_decode',
    'strict_response_shape',
    'strict_response_trailing',
    'supervisor_group',
    'synthetic_home_not_empty',
    'target_journal_count',
    'target_original_journal',
    'target_policy',
    'template',
    'transcript_binding',
    'unexpected_panic',
    'unexpected_service',
    'unknown_cli_ordinal',
    'unmodified_legacy_receive_state',
    'unrelated_policy_changed',
    'unreviewed_product_file',
    'unused_current_review',
    'unused_review_identity',
    'unused_review_restored',
    'upgrade_exact_intent',
    'upgrade_not_terminal',
    'upgrade_progress_binding',
    'upgrade_review',
    'upgrade_spacing_budget',
    'upgrade_terminal_failure',
    'upgrade_union',
    'verification_schedule',
    'verification_union',
)


def _public_require(ok):
    if not ok:
        # Never echo an untrusted value, path, key, exception, or receipt.
        raise ValueError('public_export_schema_rejected')


def _public_keys(value, keys):
    _public_require(type(value) is dict and set(value) == set(keys))


def _public_int(value, maximum, minimum=0):
    _public_require(type(value) is int and minimum <= value <= maximum)


def _public_bool(value):
    _public_require(type(value) is bool)


def _public_enum(value, choices):
    _public_require(type(value) is str and value in choices)


def _public_hash(value, length=64, nullable=False):
    if nullable and value is None:
        return
    _public_require(type(value) is str and re.fullmatch('[0-9a-f]{' + str(length) + '}', value) is not None)


def _public_exact(value, expected):
    _public_require(type(value) is type(expected) and value == expected)


def _public_no_duplicates(pairs):
    result = {}
    for key, value in pairs:
        _public_require(key not in result)
        result[key] = value
    return result


def _public_no_constant(value):
    raise ValueError('public_export_schema_rejected')


def _public_decode(raw, limit):
    _public_require(type(raw) is bytes and len(raw) <= limit)
    try:
        return json.loads(raw.decode('utf-8'), object_pairs_hook=_public_no_duplicates,
                          parse_constant=_public_no_constant)
    except (ValueError, UnicodeError, RecursionError):
        raise ValueError('public_export_schema_rejected') from None


def _public_encoded(value):
    return (json.dumps(value, sort_keys=True, indent=2, ensure_ascii=True,
                       allow_nan=False) + '\n').encode('ascii')


def _public_source(source):
    _public_keys(source, PUBLIC_SOURCE_KEYS)
    for key in ('commit', 'tree', 'parent', 'parentTree'):
        _public_hash(source[key], 40)
    for key in ('baselineProjectionSHA256', 'sourceManifestSHA256'):
        _public_hash(source[key])


def _public_approved_inventories(approved):
    # The caller must obtain this allowlist from the separately pinned anchor
    # and approved diagnostic tree, never from a fresh observed inventory.
    _public_keys(approved, PUBLIC_INVENTORY_ROLES)
    roots = {'source': ('source',), 'toolchain': ('toolchain',),
             'dependencies': ('modules', 'adapted'), 'generatedMain': ('generated',)}
    caps = {'source': 1474, 'toolchain': 15639, 'dependencies': 7536, 'generatedMain': 3}
    for role in PUBLIC_INVENTORY_ROLES:
        mapping = approved[role]
        _public_require(type(mapping) is dict and 0 < len(mapping) <= caps[role])
        for path, digest in mapping.items():
            _public_require(type(path) is str and 0 < len(path.encode('utf-8')) <= 1024)
            _public_require(not any(ord(c) < 32 or ord(c) == 127 for c in path))
            _public_require('\\' not in path and ':' not in path and '%' not in path)
            parts = path.split('/')
            _public_require(len(parts) >= 2 and parts[0] in roots[role])
            _public_require(all(part not in ('', '.', '..') for part in parts))
            _public_hash(digest)


def _public_observed_maps(observed, approved):
    _public_keys(observed, PUBLIC_INVENTORY_ROLES)
    for role in PUBLIC_INVENTORY_ROLES:
        if observed[role] is None:
            continue
        _public_keys(observed[role], approved[role])
        for digest in observed[role].values():
            _public_hash(digest)


def _public_inner_bindings(context):
    build = context['buildInfo']['product']
    return {
        'source': context['source']['commit'], 'tree': context['source']['tree'],
        'sourceManifest': context['source']['sourceManifestSHA256'],
        'binary': None if build is None else build['imageSHA256'],
        'assets': context['hashes']['assets'],
        'dependencies': context['hashes']['dependencyManifest'],
        'toolchain': context['hashes']['toolchainManifest'],
    }


def _public_validate_fixture_receipt(receipt, bindings, outcome):
    fixed = {
        'scenario': 'source-built-controller-restart-v1', 'target': 'linux-amd64',
        'selector': '^' + PUBLIC_TEST_NAME + '$', 'outcome': outcome,
        'evidenceScope': 'instrumented source-built Linux entry only; no installed-binary or other-target acceptance',
        'receiveState': 'legacy_review_required; no transfer acceptance or repair',
    }
    boolean_keys = ('originalExpiryPreserved', 'stableIdPreserved', 'historyEqual', 'noReplay', 'joined')
    _public_keys(bindings, ('source', 'tree', 'sourceManifest', 'binary', 'assets', 'dependencies', 'toolchain'))
    _public_keys(receipt, tuple(fixed) + tuple(bindings) + boolean_keys +
                 ('stage', 'owners', 'clis', 'checkpoints', 'maintenance', 'unjoined'))
    for key, value in fixed.items():
        _public_exact(receipt[key], value)
    for key, value in bindings.items():
        _public_hash(value, 40 if key in ('source', 'tree') else 64)
        _public_exact(receipt[key], value)
    for key in boolean_keys:
        _public_bool(receipt[key])
    for key, maximum in (('owners', 5), ('clis', 40), ('checkpoints', 26), ('maintenance', (1 << 64) - 1)):
        _public_int(receipt[key], maximum)
    _public_keys(receipt['unjoined'], PUBLIC_UNJOINED_BOUNDS)
    for key, maximum in PUBLIC_UNJOINED_BOUNDS.items():
        _public_int(receipt['unjoined'][key], maximum)
    if outcome == 'failed':
        _public_enum(receipt['stage'], PUBLIC_FAILURE_STAGES)
        for key in boolean_keys[:-1]:
            _public_exact(receipt[key], False)
        if receipt['joined']:
            _public_require(receipt['owners'] == 5 and receipt['clis'] == 40)
    else:
        _public_exact(receipt['stage'], '')
        _public_require(receipt['owners'] == 5 and receipt['clis'] == 40 and receipt['checkpoints'] == 26)
        _public_require(receipt['maintenance'] >= 2 and all(receipt[key] for key in boolean_keys))
        _public_require(not any(receipt['unjoined'].values()))
    # On failure finalReceipt.joined measures child joins, while unjoined also
    # includes separate registered handles/driver/reservations. Keep both facts.
    return receipt


def validate_failure_receipt(data, bindings):
    """Validate failed P1 JSON bytes independently of the unchanged success oracle.

    Requiring bytes preserves duplicate-key detection. bindings is the exact
    seven-field object derived from the admitted source and validated manifest.
    An early failure with missing provenance is rejected, never filled in.
    """
    return _public_validate_fixture_receipt(_public_decode(data, 16384), bindings, 'failed')


def validate_failure_stdout(raw, bindings):
    """Only the complete fixed five-line failed-test grammar yields observations.

    This does not read a file or inspect child stderr and cannot certify success.
    Extra output, duplicate receipts, skipped tests, or partial output fail closed.
    """
    _public_require(type(raw) is bytes and len(raw) <= 32768)
    try:
        lines = raw.decode('utf-8').splitlines()
    except UnicodeError:
        raise ValueError('public_export_schema_rejected') from None
    _public_require(len(lines) == 5 and lines[0] == '=== RUN   ' + PUBLIC_TEST_NAME and lines[4] == 'FAIL')
    match = re.fullmatch(r'    resource_process_acceptance_native_test.go:[0-9]+: (\{.*\})', lines[1])
    _public_require(match is not None)
    receipt = validate_failure_receipt(match.group(1).encode('utf-8'), bindings)
    _public_require(re.fullmatch(r'    resource_process_acceptance_native_test.go:[0-9]+: '
                    r'P1 failed; protected evidence retained; stage=' + re.escape(receipt['stage']), lines[2]) is not None)
    _public_require(re.fullmatch(r'--- FAIL: ' + PUBLIC_TEST_NAME + r' \([0-9]+(?:\.[0-9]+)?s\)', lines[3]) is not None)
    return receipt


def _public_build_info(build_info, source):
    _public_keys(build_info, PUBLIC_IMAGE_ROLES)
    for role in PUBLIC_IMAGE_ROLES:
        info = build_info[role]
        if info is None:
            continue
        _public_keys(info, PUBLIC_BUILD_KEYS)
        fixed = {
            'goVersion': 'go1.27.1', 'modulePath': 'github.com/webkaz-labs/sobalink',
            'packagePath': PUBLIC_PACKAGE_PATHS[role],
            'revision': source['commit'], 'vcsModified': False, 'goos': 'linux',
            'goarch': 'amd64', 'goamd64': 'v1', 'cgoEnabled': False, 'trimpath': True,
            'buildvcs': True, 'pgo': 'off', 'tags': PUBLIC_TAGS, 'buildMode': 'exe',
            'compiler': 'gc', 'settingsVerified': True,
        }
        for key, value in fixed.items():
            _public_exact(info[key], value)
        _public_require(type(info['moduleVersion']) is str and re.fullmatch(
            r'v0\.0\.0-[0-9]{14}-' + source['commit'][:12], info['moduleVersion']) is not None)
        _public_hash(info['imageSHA256'])


def _public_outer(outer):
    _public_keys(outer, PUBLIC_OUTER_KEYS)
    for key in PUBLIC_OUTER_BOOL_KEYS:
        _public_bool(outer[key])
    if outer['leaderExitObservedBeforeOuterDeadline'] is not None:
        _public_bool(outer['leaderExitObservedBeforeOuterDeadline'])
    _public_int(outer['groupSignals'], 1)
    # The retained group can already be absent when the unconditional sweep
    # is attempted. That valid attempt sends zero signals; it is not a retry.
    _public_require(outer['groupSignals'] == 0 or outer['groupSignalAttempted'])
    if outer['returncode'] is not None:
        _public_int(outer['returncode'], 255, -128)
        _public_require(outer['invocationAttempted'] and outer['processJoined'])
    if outer['commandAndJoinElapsedMicros'] is not None:
        _public_int(outer['commandAndJoinElapsedMicros'], 3600 * 1000000)
    _public_int(outer['rawOutputSeenBytes'], (1 << 63) - 1)
    _public_int(outer['rawOutputRetainedBytes'], 8 * 1024 * 1024)
    _public_require(outer['rawOutputRetainedBytes'] <= outer['rawOutputSeenBytes'])
    if not outer['invocationAttempted']:
        _public_require(not any(outer[key] for key in ('processJoined', 'readersJoined', 'ownedGroupAbsent',
                            'groupSignalAttempted', 'fixtureRootTerminallyAbsent', 'lateOutputObserved', 'allChecksPassed')))
        _public_require(outer['returncode'] is None and outer['leaderExitObservedBeforeOuterDeadline'] is None)
        _public_require(outer['rawOutputSeenBytes'] == outer['rawOutputRetainedBytes'] == 0)
    if outer['allChecksPassed']:
        _public_require(all(outer[key] for key in ('invocationAttempted', 'processJoined', 'readersJoined',
                         'ownedGroupAbsent', 'groupSignalAttempted', 'inputsUnchanged',
                         'sourceIdentityUnchanged', 'fixtureRootTerminallyAbsent')))
        _public_require(outer['leaderExitObservedBeforeOuterDeadline'] is True and outer['returncode'] == 0
                        and not outer['lateOutputObserved'])
        _public_require(outer['commandAndJoinElapsedMicros'] is not None
                        and outer['commandAndJoinElapsedMicros'] <= 370 * 1000000)


def _public_context(context, approved):
    _public_keys(context, ('phase', 'outcome', 'failureCategory', 'source', 'hashes', 'actionPins',
                 'image', 'toolHashes', 'toolVersions', 'buildInfo', 'prerequisites', 'outer',
                 'testOutput', 'innerReceipt', 'before', 'after'))
    _public_approved_inventories(approved)
    _public_source(context['source'])
    _public_enum(context['phase'], PUBLIC_PHASES)
    _public_enum(context['outcome'], ('passed', 'failed', 'not_run'))
    _public_enum(context['failureCategory'], PUBLIC_FAILURE_CATEGORIES)
    _public_keys(context['hashes'], PUBLIC_HASH_KEYS)
    for digest in context['hashes'].values():
        _public_hash(digest, nullable=True)
    _public_keys(context['actionPins'], PUBLIC_ACTION_PINS)
    for key, value in PUBLIC_ACTION_PINS.items():
        _public_exact(context['actionPins'][key], value)
    image = context['image']
    fixed_image = {'label': 'ubuntu-24.04', 'ImageOS': 'ubuntu24', 'ImageVersion': '20261004.327.1',
                   'runnerEnvironment': 'github-hosted', 'architecture': 'X64'}
    _public_keys(image, fixed_image)
    for key, value in fixed_image.items():
        _public_exact(image[key], value)
    _public_keys(context['toolHashes'], PUBLIC_TOOL_HASH_KEYS)
    for role, digest in context['toolHashes'].items():
        _public_hash(digest, nullable=role == 'go')
    versions = context['toolVersions']
    _public_keys(versions, ('python', 'git', 'go'))
    for key in ('python', 'git'):
        _public_require(type(versions[key]) is str and re.fullmatch(r'[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}', versions[key]) is not None)
    if versions['go'] is not None:
        _public_exact(versions['go'], 'go1.27.1')
    _public_build_info(context['buildInfo'], context['source'])
    _public_observed_maps(context['before'], approved)
    _public_observed_maps(context['after'], approved)
    prerequisites = context['prerequisites']
    _public_keys(prerequisites, ('validationScope', 'verifiedRun', 'verifiedPass', 'completedCohorts',
                                'unclassifiedOrNotRunCohorts', 'allChecksPassed'))
    _public_exact(prerequisites['validationScope'], 'completed_successful_cohorts_only')
    for key in ('verifiedRun', 'verifiedPass'):
        _public_int(prerequisites[key], 83)
    for key in ('completedCohorts', 'unclassifiedOrNotRunCohorts'):
        _public_int(prerequisites[key], 5)
    _public_bool(prerequisites['allChecksPassed'])
    _public_require(prerequisites['verifiedRun'] == prerequisites['verifiedPass'])
    # The five exact cohort sizes also constrain partial reports. Runtime state
    # retains and independently validates the corresponding fixed cohort IDs.
    sizes=(32,37,11,1,2)
    possible={(sum(bool(mask & (1<<index)) for index in range(5)),
               sum(size for index,size in enumerate(sizes) if mask & (1<<index))) for mask in range(32)}
    _public_require((prerequisites['completedCohorts'],prerequisites['verifiedRun']) in possible)
    _public_require(prerequisites['completedCohorts'] + prerequisites['unclassifiedOrNotRunCohorts'] == 5)
    _public_exact(prerequisites['allChecksPassed'], prerequisites['completedCohorts'] == 5
                  and prerequisites['verifiedRun'] == prerequisites['verifiedPass'] == 83)
    outer = context['outer']
    _public_outer(outer)
    tests = context['testOutput']
    _public_keys(tests, ('run', 'pass', 'fail', 'notRun', 'syntaxValidated'))
    for key in ('run', 'pass', 'fail', 'notRun'):
        _public_int(tests[key], 1)
    _public_bool(tests['syntaxValidated'])
    _public_require(tests['pass'] + tests['fail'] <= tests['run'])
    _public_require(tests['notRun'] == int(not outer['invocationAttempted']))
    if tests['notRun'] or not tests['syntaxValidated']:
        _public_require(tests['run'] == tests['pass'] == tests['fail'] == 0)
    inner = context['innerReceipt']
    _public_exact(tests['syntaxValidated'], inner is not None)
    if inner is not None:
        _public_require(type(inner) is dict and inner.get('outcome') in ('passed', 'failed'))
        _public_validate_fixture_receipt(inner, _public_inner_bindings(context), inner['outcome'])
        _public_require(outer['invocationAttempted'] and outer['processJoined'] and outer['readersJoined']
                        and outer['ownedGroupAbsent'] and tests['syntaxValidated'])
        _public_require(tests['run'] == 1 and tests['pass'] == int(inner['outcome'] == 'passed')
                        and tests['fail'] == int(inner['outcome'] == 'failed'))
    else:
        _public_require(tests['pass'] == tests['fail'] == 0)
    if context['outcome'] == 'passed' or outer['allChecksPassed']:
        _public_require(context['outcome'] == 'passed' and context['failureCategory'] == 'none'
                        and prerequisites['allChecksPassed'] and outer['allChecksPassed']
                        and inner is not None and inner['outcome'] == 'passed')
        _public_require(all(context['hashes'][key] is not None for key in PUBLIC_HASH_KEYS)
                        and all(context['buildInfo'][role] is not None for role in PUBLIC_IMAGE_ROLES))
        _public_require(all(context['before'][role] == approved[role] == context['after'][role]
                            for role in PUBLIC_INVENTORY_ROLES))
        _public_require(context['hashes']['runtimeInputsBefore'] == context['hashes']['runtimeInputsAfter'])
    else:
        _public_require(context['failureCategory'] != 'none')
    if context['outcome'] == 'not_run':
        _public_require(not outer['invocationAttempted'])


def _public_objects(context, approved):
    inner = context['innerReceipt']
    observed = {'ownerRecords': None, 'cliInvocationsScheduled': None, 'checkpoints': None,
                'maintenanceDelta': None, 'nodeAllocations': None}
    if inner is not None:
        observed.update(ownerRecords=inner['owners'], cliInvocationsScheduled=inner['clis'],
                        checkpoints=inner['checkpoints'], maintenanceDelta=inner['maintenance'])
    result = {'schema': 1, 'phase': context['phase'], 'outcome': context['outcome'],
              'failureCategory': context['failureCategory'], 'source': context['source'],
              'hashes': context['hashes'], 'prerequisites': context['prerequisites'],
              'outer': context['outer'], 'testOutput': context['testOutput'],
              'fixtureReceipt': inner, 'observedCounts': observed,
              'intendedCounts': PUBLIC_INTENDED_COUNTS, 'claimLimits': PUBLIC_CLAIM_LIMITS}
    provenance = {'schema': 1, 'source': context['source'], 'hashes': context['hashes'],
                  'actionPins': context['actionPins'], 'goAcquisitionPolicy': PUBLIC_GO_ACQUISITION_POLICY,
                  'image': context['image'],
                  'toolHashes': context['toolHashes'], 'toolVersions': context['toolVersions'],
                  'imageTrust': 'standard_official_github_hosted_ubuntu',
                  'observationLimit': 'host_tools_and_os_are_observed_not_independently_byte_authenticated',
                  'inventoryFormat': 'sorted_key_indent2_ascii_json_final_newline_role_relative_sha256_v1',
                  'manifestRebinding': 'normalized_role_paths_do_not_claim_historical_digest_continuity',
                  'inputCounts': {role: len(approved[role]) for role in PUBLIC_INVENTORY_ROLES}}
    equality = {}
    for role in PUBLIC_INVENTORY_ROLES:
        before, after = context['before'][role], context['after'][role]
        equality[role] = {
            'beforeMatchesApproved': None if before is None else before == approved[role],
            'afterMatchesApproved': None if after is None else after == approved[role],
            'beforeAfterEqual': None if before is None or after is None else before == after,
        }
    inputs = {'schema': 1, 'format': 'role_relative_sha256_v1', 'approved': approved,
              'before': context['before'], 'after': context['after'], 'equalities': equality}
    tests = context['testOutput']
    summary = (PUBLIC_TEST_NAME + '\nRUN=' + str(tests['run']) + '\nPASS=' + str(tests['pass'])
               + '\nFAIL=' + str(tests['fail']) + '\nNOT_RUN=' + str(tests['notRun'])
               + '\nOUTPUT_VALIDATED=' + ('true' if tests['syntaxValidated'] else 'false') + '\n').encode('ascii')
    return {'result.json': _public_encoded(result), 'run-pass-summary.txt': summary,
            'provenance.json': _public_encoded(provenance),
            'build-info.json': _public_encoded({'schema': 1, 'images': context['buildInfo']}),
            'input-hashes.json': _public_encoded(inputs),
            'child-error-category.json': _public_encoded({'schema': 1, 'category': 'unclassified',
                                                        'inspected': False})}


def construct_public_artifacts(context, approved_inventories):
    """Return exactly six canonical public filename-to-bytes entries, or fail.

    Caller contract: source is admitted H/tree/sole public parent/parent tree plus
    baselineProjectionSHA256 and sourceManifestSHA256. actionPins and image are
    exact reviewed values. toolHashes has only named ordinary hosted observations;
    toolVersions contains numeric Python/Git versions and optional go1.27.1.
    hashes has the seven PUBLIC_HASH_KEYS with SHA256 or null for uncompleted work.
    buildInfo maps four PUBLIC_IMAGE_ROLES to validated closed build fields or null.
    before/after contain exactly PUBLIC_INVENTORY_ROLES with complete path/hash maps
    or null; approved_inventories is independently anchor/tree-bound, never inferred
    from these observations. This pure helper does not establish that trust itself.
    prerequisites reports only exact validated successful cohorts; incomplete or
    unclassified cohorts never imply zero observed failures/skips.
    outer is a fresh projection of exactly PUBLIC_OUTER_KEYS, never a raw receipt;
    original floating elapsed seconds must be converted to bounded integer micros.
    testOutput contains run/pass/fail/notRun integers and syntaxValidated boolean,
    from the unchanged success validator or validate_failure_stdout; malformed
    output uses zero observed counts and syntaxValidated=false. innerReceipt is
    the exact validated receipt or null, and never processAccounting. Observed
    owner records and scheduled CLIs do not imply readiness or completed actions.
    Node allocation counts are not independently observed and remain null.
    The first-owner classifier is deliberately omitted: no child file is read.
    The caller must validate returned files again immediately before export-ready.
    """
    _public_context(context, approved_inventories)
    data = _public_objects(context, approved_inventories)
    _public_require(sum(len(raw) for raw in data.values()) <= PUBLIC_EXPORT_LIMIT)
    return data


def validate_public_artifacts(data, approved_inventories):
    """Independently reparse and reconstruct all six files; reject every extra byte.

    Requires the same independent approved inventories as construction. It rejects
    duplicate JSON keys, unknown fields/paths, booleans masquerading as counts,
    private-shaped strings, inconsistent observations, noncanonical JSON and an
    aggregate larger than 16 MiB. It has no I/O or process effects.
    """
    _public_keys(data, PUBLIC_ARTIFACT_NAMES)
    _public_require(all(type(raw) is bytes for raw in data.values()))
    _public_require(sum(len(raw) for raw in data.values()) <= PUBLIC_EXPORT_LIMIT)
    result = _public_decode(data['result.json'], PUBLIC_EXPORT_LIMIT)
    provenance = _public_decode(data['provenance.json'], PUBLIC_EXPORT_LIMIT)
    build = _public_decode(data['build-info.json'], PUBLIC_EXPORT_LIMIT)
    inputs = _public_decode(data['input-hashes.json'], PUBLIC_EXPORT_LIMIT)
    child = _public_decode(data['child-error-category.json'], 1024)
    _public_keys(result, ('schema', 'phase', 'outcome', 'failureCategory', 'source', 'hashes',
                         'prerequisites', 'outer', 'testOutput', 'fixtureReceipt', 'observedCounts',
                         'intendedCounts', 'claimLimits'))
    _public_keys(provenance, ('schema', 'source', 'hashes', 'actionPins', 'image', 'toolHashes',
                             'toolVersions', 'goAcquisitionPolicy', 'imageTrust', 'observationLimit', 'inventoryFormat',
                             'manifestRebinding', 'inputCounts'))
    _public_keys(build, ('schema', 'images'))
    _public_keys(inputs, ('schema', 'format', 'approved', 'before', 'after', 'equalities'))
    _public_keys(child, ('schema', 'category', 'inspected'))
    for item in (result, provenance, build, inputs, child):
        _public_exact(item['schema'], 1)
    context = {
        'phase': result['phase'], 'outcome': result['outcome'], 'failureCategory': result['failureCategory'],
        'source': result['source'], 'hashes': result['hashes'], 'actionPins': provenance['actionPins'],
        'image': provenance['image'], 'toolHashes': provenance['toolHashes'],
        'toolVersions': provenance['toolVersions'], 'buildInfo': build['images'],
        'prerequisites': result['prerequisites'], 'outer': result['outer'],
        'testOutput': result['testOutput'], 'innerReceipt': result['fixtureReceipt'],
        'before': inputs['before'], 'after': inputs['after'],
    }
    expected = construct_public_artifacts(context, approved_inventories)
    _public_require(all(data[name] == expected[name] for name in PUBLIC_ARTIFACT_NAMES))
    return True


# One exact official archive; no arbitrary URL, target, version or installer.
GO_ARCHIVE_URL='https://dl.google.com/go/go1.27.1.linux-amd64.tar.gz'
GO_ARCHIVE_SHA256='63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445'
GO_ARCHIVE_BYTES=70553950
GO_ARCHIVE_FILE_BYTES=243910739
GO_PAX_PATHS=('go/test/fixedbugs/issue27836.dir/Þfoo.go','go/test/fixedbugs/issue27836.dir/Þmain.go')


def validate_archive_member_values(value):
    """Pure closed member-value admission, also exercised by fixed-value tests."""
    require(type(value) is dict and set(value)=={'name','kind','size','mode','linkname','pax','sparse'},
            'archive_member_schema')
    name=value['name'];safe_relative(name)
    require(name=='go' or name.startswith('go/'),'archive_member_root')
    require(value['kind'] in ('file','directory') and value['linkname']=='' and value['sparse'] is False,
            'archive_member_type')
    require(type(value['size']) is int and 0<=value['size']<=32<<20,'archive_member_size')
    require(type(value['mode']) is int,'archive_member_mode')
    if value['kind']=='directory':
        require(value['size']==0 and value['mode']==0o755,'archive_directory_shape')
    else:
        require(name!='go' and value['mode'] in (0o644,0o755),'archive_file_mode')
    expected={'path':name} if name in GO_PAX_PATHS else {}
    require(type(value['pax']) is dict and value['pax']==expected,'archive_pax_not_approved')
    return value


def acquire_official_go():
    import ssl
    import tarfile
    import urllib.request
    data=reload_admission();anchor=load_anchor()
    os.umask(0o077)
    save_new(OUT/'acquisition-started.json',{'attempt':1,'archiveSHA256':GO_ARCHIVE_SHA256},16384)
    started=time.monotonic();deadline=started+420
    def remaining():
        left=deadline-time.monotonic()
        require(left>0,'acquisition_deadline')
        return min(15,left)
    class RejectRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self,req,fp,code,msg,headers,newurl):
            raise RuntimeError('archive_redirect_rejected')
    context=ssl.create_default_context(cafile='/etc/ssl/certs/ca-certificates.crt')
    require(context.check_hostname and context.verify_mode==ssl.CERT_REQUIRED,'tls_verification_required')
    opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),
        urllib.request.HTTPSHandler(context=context),RejectRedirect())
    request=urllib.request.Request(GO_ARCHIVE_URL,headers={'Accept-Encoding':'identity',
        'User-Agent':'sobalink-p1-source-diagnostic'},method='GET')
    path=OUT/'go1.27.1.linux-amd64.tar.gz'
    count=0;digest=hashlib.sha256()
    with opener.open(request,timeout=remaining()) as response:
        require(response.status==200 and response.geturl()==GO_ARCHIVE_URL,'archive_http_response')
        require(response.headers.get('Content-Encoding','identity')=='identity','archive_encoding_changed')
        length=response.headers.get('Content-Length')
        require(length is None or length==str(GO_ARCHIVE_BYTES),'archive_content_length')
        # CPython's official hosted HTTPS response socket is retained; update
        # its timeout with the original remaining deadline before every read1.
        tls_socket=response.fp.raw._sock
        require(isinstance(tls_socket,ssl.SSLSocket) and tls_socket.server_hostname=='dl.google.com',
                'archive_tls_socket_changed')
        fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW|os.O_CLOEXEC,0o600)
        with os.fdopen(fd,'wb') as sink:
            while True:
                tls_socket.settimeout(remaining())
                block=response.read1(65536)
                remaining()
                if not block:break
                require(count+len(block)<=GO_ARCHIVE_BYTES,'archive_download_overflow')
                sink.write(block);digest.update(block);count+=len(block)
    require(count==GO_ARCHIVE_BYTES and digest.hexdigest()==GO_ARCHIVE_SHA256,'archive_digest_mismatch')
    fd=os.open(path,os.O_RDONLY|os.O_NOFOLLOW|os.O_CLOEXEC)
    with os.fdopen(fd,'rb') as archive:
        initial=os.fstat(archive.fileno())
        require(stat.S_ISREG(initial.st_mode) and stat.S_IMODE(initial.st_mode)==0o600
                and initial.st_uid==os.geteuid() and initial.st_nlink==1
                and initial.st_size==GO_ARCHIVE_BYTES,'archive_identity_invalid')
        identity=(initial.st_dev,initial.st_ino,initial.st_size,initial.st_mtime_ns,initial.st_ctime_ns)
        def retain_archive():
            current=os.fstat(archive.fileno());named=path.lstat()
            require((current.st_dev,current.st_ino,current.st_size,current.st_mtime_ns,current.st_ctime_ns)==identity
                    and (named.st_dev,named.st_ino)==identity[:2] and stat.S_ISREG(named.st_mode),
                    'archive_identity_changed')
            archive.seek(0);h=hashlib.sha256()
            while True:
                remaining();block=archive.read(1<<20)
                if not block:break
                h.update(block)
            require(h.hexdigest()==GO_ARCHIVE_SHA256,'archive_digest_changed')
            archive.seek(0)
        retain_archive()
        members=[];names=set();file_names=set();directory_names=set();logical=0
        with tarfile.open(fileobj=archive,mode='r:gz') as container:
            for member in container:
                remaining()
                require(len(members)<17353 and member.name not in names,'archive_duplicate_or_count')
                require(member.type in (tarfile.REGTYPE,tarfile.AREGTYPE,tarfile.DIRTYPE),
                        'archive_special_member')
                value={'name':member.name,'kind':'directory' if member.isdir() else 'file',
                       'size':member.size,'mode':member.mode,'linkname':member.linkname,
                       'pax':dict(member.pax_headers),'sparse':bool(member.sparse)}
                validate_archive_member_values(value)
                names.add(member.name);members.append(value)
                if member.isfile():file_names.add('toolchain/'+member.name.removeprefix('go/'));logical+=member.size
                else:directory_names.add(member.name)
                require(logical<=GO_ARCHIVE_FILE_BYTES,'archive_uncompressed_limit')
        require(len(members)==17353 and len(file_names)==15639 and len(directory_names)==1714
                and logical==GO_ARCHIVE_FILE_BYTES and file_names==set(anchor['toolchainFiles']),
                'archive_complete_inventory_mismatch')
        parents={p.as_posix() for value in members if value['kind']=='file'
                 for p in pathlib.PurePosixPath(value['name']).parents if p.as_posix()!='.'}
        require(directory_names==parents,'archive_directory_closure_mismatch')
        retain_archive()  # Same authenticated descriptor across both passes.
        tool_parent=GOROOT.parent.parent
        for parent in [tool_parent,*tool_parent.parents]:
            require(not stat.S_ISLNK(parent.lstat().st_mode),'toolcache_parent_linked')
        parent_facts=tool_parent.lstat()
        require(stat.S_ISDIR(parent_facts.st_mode) and parent_facts.st_uid==os.geteuid(),
                'toolcache_parent_not_owned')
        parent_identity=(parent_facts.st_dev,parent_facts.st_ino)
        require(not os.path.lexists(GOROOT.parent),'toolchain_version_already_present')
        GOROOT.parent.mkdir(mode=0o700);GOROOT.mkdir(mode=0o700)
        root_identity=(GOROOT.stat().st_dev,GOROOT.stat().st_ino)
        for name in sorted(directory_names-{'go'},key=lambda v:(len(pathlib.PurePosixPath(v).parts),v)):
            remaining();(GOROOT/name.removeprefix('go/')).mkdir(mode=0o700)
        with tarfile.open(fileobj=archive,mode='r:gz') as container:
            for index,member in enumerate(container):
                remaining()
                require(index<len(members),'archive_second_pass_count')
                value={'name':member.name,'kind':'directory' if member.isdir() else 'file',
                       'size':member.size,'mode':member.mode,'linkname':member.linkname,
                       'pax':dict(member.pax_headers),'sparse':bool(member.sparse)}
                require(value==members[index],'archive_second_pass_changed')
                if not member.isfile():continue
                dest=GOROOT/member.name.removeprefix('go/')
                mode=0o700 if member.mode==0o755 else 0o600
                target=os.open(dest,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW|os.O_CLOEXEC,mode)
                with os.fdopen(target,'wb') as sink,container.extractfile(member) as source:
                    copied=0;h=hashlib.sha256()
                    while True:
                        remaining();block=source.read(65536)
                        if not block:break
                        require(copied+len(block)<=member.size,'extraction_file_overflow')
                        sink.write(block);h.update(block);copied+=len(block)
                    require(copied==member.size and h.hexdigest()==anchor['toolchainFiles'][
                            'toolchain/'+member.name.removeprefix('go/')],'extracted_file_mismatch')
        require(index+1==17353,'archive_second_pass_count')
        retain_archive()
        root=GOROOT.lstat();parent=tool_parent.lstat()
        require((root.st_dev,root.st_ino)==root_identity and (parent.st_dev,parent.st_ino)==parent_identity,
                'toolcache_identity_changed')
    exact_toolchain(anchor)
    require(source_inventory(anchor)==data['sourceRecords'],'acquisition_source_drift')
    remaining();sample_materialization()
    save_new(OUT/'acquisition-complete.json',{'archiveBytes':count,'archiveSHA256':GO_ARCHIVE_SHA256,
        'toolchainManifestSHA256':TOOLCHAIN_SHA256,'files':15639,'elapsedMicros':int((time.monotonic()-started)*1000000)},16384)
    print('phase=acquired')


if __name__ == '__main__':
    try:
        raise SystemExit(main())
    except Exception:
        # Never emit traceback, exception text, environment or paths.
        print('phase=blocked outcome=failed')
        raise SystemExit(1) from None
