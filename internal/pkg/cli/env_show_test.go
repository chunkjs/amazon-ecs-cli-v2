// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/aws/amazon-ecs-cli-v2/internal/pkg/config"
	"github.com/aws/amazon-ecs-cli-v2/internal/pkg/term/color"

	"testing"

	"github.com/aws/amazon-ecs-cli-v2/internal/pkg/cli/mocks"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
)

type showEnvMocks struct {
	storeSvc  *mocks.Mockstore
	prompt    *mocks.Mockprompter
	describer *mocks.MockenvDescriber
	sel       *mocks.MockconfigSelector
}

func TestEnvShow_Validate(t *testing.T) {
	testCases := map[string]struct {
		inputApp         string
		inputEnvironment string
		setupMocks       func(mocks showEnvMocks)

		wantedError error
	}{
		"valid app name and environment name": {
			inputApp:         "my-app",
			inputEnvironment: "my-env",

			setupMocks: func(m showEnvMocks) {
				gomock.InOrder(
					m.storeSvc.EXPECT().GetApplication("my-app").Return(&config.Application{
						Name: "my-app",
					}, nil),
					m.storeSvc.EXPECT().GetEnvironment("my-app", "my-env").Return(&config.Environment{
						Name: "my-env",
					}, nil),
				)
			},

			wantedError: nil,
		},
		"invalid app name": {
			inputApp:         "my-app",
			inputEnvironment: "my-env",

			setupMocks: func(m showEnvMocks) {
				m.storeSvc.EXPECT().GetApplication("my-app").Return(nil, errors.New("some error"))
			},

			wantedError: fmt.Errorf("some error"),
		},
		"invalid environment name": {
			inputApp:         "my-app",
			inputEnvironment: "my-env",

			setupMocks: func(m showEnvMocks) {
				gomock.InOrder(
					m.storeSvc.EXPECT().GetApplication("my-app").Return(&config.Application{
						Name: "my-app",
					}, nil),
					m.storeSvc.EXPECT().GetEnvironment("my-app", "my-env").Return(nil, errors.New("some error")),
				)
			},

			wantedError: fmt.Errorf("some error"),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStoreReader := mocks.NewMockstore(ctrl)

			mocks := showEnvMocks{
				storeSvc: mockStoreReader,
			}

			tc.setupMocks(mocks)

			showEnvs := &showEnvOpts{
				showEnvVars: showEnvVars{
					envName: tc.inputEnvironment,
					GlobalOpts: &GlobalOpts{
						appName: tc.inputApp,
					},
				},
				store: mockStoreReader,
			}

			// WHEN
			err := showEnvs.Validate()

			// THEN
			if tc.wantedError != nil {
				require.EqualError(t, err, tc.wantedError.Error())
			} else {
				require.Nil(t, err)
			}
		})
	}
}

func TestEnvShow_Ask(t *testing.T) {
	mockErr := errors.New("some error")
	testCases := map[string]struct {
		inputApp string
		inputEnv string

		setupMocks func(mocks showEnvMocks)

		wantedApp   string
		wantedEnv   string
		wantedError error
	}{
		"with all flags": {
			inputApp: "my-app",
			inputEnv: "my-env",

			setupMocks: func(mocks showEnvMocks) {},

			wantedApp: "my-app",
			wantedEnv: "my-env",
		},
		"returns error when fail to select app": {
			inputApp: "",
			inputEnv: "",

			setupMocks: func(m showEnvMocks) {
				m.sel.EXPECT().Application(envShowAppNamePrompt, envShowAppNameHelpPrompt).Return("", mockErr)
			},

			wantedError: fmt.Errorf("select application: some error"),
		},
		"returns error when fail to select environment": {
			inputApp: "my-app",
			inputEnv: "",

			setupMocks: func(m showEnvMocks) {
				m.sel.EXPECT().Environment(fmt.Sprintf(envShowNamePrompt, color.HighlightUserInput("my-app")), envShowHelpPrompt, "my-app").Return("", mockErr)
			},

			wantedError: fmt.Errorf("select environment for application my-app: some error"),
		},
		"success with no flag set": {
			inputApp: "",
			inputEnv: "",

			setupMocks: func(m showEnvMocks) {
				gomock.InOrder(
					m.sel.EXPECT().Application(envShowAppNamePrompt, envShowAppNameHelpPrompt).Return("my-app", nil),
					m.sel.EXPECT().Environment(fmt.Sprintf(envShowNamePrompt, color.HighlightUserInput("my-app")), envShowHelpPrompt, "my-app").Return("my-env", nil),
				)
			},

			wantedApp: "my-app",
			wantedEnv: "my-env",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockSelector := mocks.NewMockconfigSelector(ctrl)

			mocks := showEnvMocks{
				sel: mockSelector,
			}

			tc.setupMocks(mocks)

			showEnvs := &showEnvOpts{
				showEnvVars: showEnvVars{
					envName: tc.inputEnv,
					GlobalOpts: &GlobalOpts{
						appName: tc.inputApp,
					},
				},
				sel: mockSelector,
			}
			// WHEN
			err := showEnvs.Ask()
			// THEN
			if tc.wantedError != nil {
				require.EqualError(t, err, tc.wantedError.Error())
			} else {
				require.Nil(t, err)
				require.Equal(t, tc.wantedApp, showEnvs.AppName(), "expected app name to match")
				require.Equal(t, tc.wantedEnv, showEnvs.envName, "expected environment name to match")
			}
		})
	}
}

func TestEnvShow_Execute(t *testing.T) {
	mockError := errors.New("some error")
	testCases := map[string]struct {
		inputEnv         string
		shouldOutputJSON bool

		mockEnvDescriber func(m *mocks.MockenvDescriber)

		wantedContent string
		wantedError   error
	}{
		"errors if failed to describe the env": {
			mockEnvDescriber: func(m *mocks.MockenvDescriber) {
				m.EXPECT().Describe().Return(nil, mockError)
			},
			wantedError: fmt.Errorf("describe environment mockEnv: some error"),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			b := &bytes.Buffer{}
			mockStoreReader := mocks.NewMockstore(ctrl)
			mockEnvDescriber := mocks.NewMockenvDescriber(ctrl)
			tc.mockEnvDescriber(mockEnvDescriber)

			showEnvs := &showEnvOpts{
				showEnvVars: showEnvVars{
					GlobalOpts:       &GlobalOpts{},
					shouldOutputJSON: tc.shouldOutputJSON,
					envName:          "mockEnv",
				},
				store:            mockStoreReader,
				describer:        mockEnvDescriber,
				initEnvDescriber: func(opts *showEnvOpts) error { return nil },
				w:                b,
			}

			// WHEN
			err := showEnvs.Execute()

			// THEN
			if tc.wantedError != nil {
				require.EqualError(t, err, tc.wantedError.Error())
			} else {
				require.Nil(t, err)
				require.Equal(t, tc.wantedContent, b.String(), "expected output content match")
			}
		})
	}
}
