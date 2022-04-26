package authority

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"

	"go.step.sm/linkedca"

	"github.com/smallstep/certificates/authority/admin"
	"github.com/smallstep/certificates/authority/administrator"
	"github.com/smallstep/certificates/authority/config"
	"github.com/smallstep/certificates/authority/policy"
	"github.com/smallstep/certificates/authority/provisioner"
	"github.com/smallstep/certificates/db"
)

func TestAuthority_checkPolicy(t *testing.T) {
	type test struct {
		ctx          context.Context
		currentAdmin *linkedca.Admin
		otherAdmins  []*linkedca.Admin
		policy       *linkedca.Policy
		err          *PolicyError
	}
	tests := map[string]func(t *testing.T) test{
		"fail/NewX509PolicyEngine-error": func(t *testing.T) test {
			return test{
				ctx: context.Background(),
				policy: &linkedca.Policy{
					X509: &linkedca.X509Policy{
						Allow: &linkedca.X509Names{
							Dns: []string{"**.local"},
						},
					},
				},
				err: &PolicyError{
					Typ: ConfigurationFailure,
					Err: errors.New("cannot parse permitted domain constraint \"**.local\": domain constraint \"**.local\" can only have wildcard as starting character"),
				},
			}
		},
		"fail/currentAdmin-evaluation-error": func(t *testing.T) test {
			return test{
				ctx:          context.Background(),
				currentAdmin: &linkedca.Admin{Subject: "*"},
				otherAdmins:  []*linkedca.Admin{},
				policy: &linkedca.Policy{
					X509: &linkedca.X509Policy{
						Allow: &linkedca.X509Names{
							Dns: []string{"*.local"},
						},
					},
				},
				err: &PolicyError{
					Typ: EvaluationFailure,
					Err: errors.New("cannot parse dns domain \"*\""),
				},
			}
		},
		"fail/currentAdmin-lockout": func(t *testing.T) test {
			return test{
				ctx:          context.Background(),
				currentAdmin: &linkedca.Admin{Subject: "step"},
				otherAdmins: []*linkedca.Admin{
					{
						Subject: "otherAdmin",
					},
				},
				policy: &linkedca.Policy{
					X509: &linkedca.X509Policy{
						Allow: &linkedca.X509Names{
							Dns: []string{"*.local"},
						},
					},
				},
				err: &PolicyError{
					Typ: AdminLockOut,
					Err: errors.New("the provided policy would lock out [step] from the CA. Please update your policy to include [step] as an allowed name"),
				},
			}
		},
		"fail/otherAdmins-evaluation-error": func(t *testing.T) test {
			return test{
				ctx:          context.Background(),
				currentAdmin: &linkedca.Admin{Subject: "step"},
				otherAdmins: []*linkedca.Admin{
					{
						Subject: "other",
					},
					{
						Subject: "**",
					},
				},
				policy: &linkedca.Policy{
					X509: &linkedca.X509Policy{
						Allow: &linkedca.X509Names{
							Dns: []string{"step", "other", "*.local"},
						},
					},
				},
				err: &PolicyError{
					Typ: EvaluationFailure,
					Err: errors.New("cannot parse dns domain \"**\""),
				},
			}
		},
		"fail/otherAdmins-lockout": func(t *testing.T) test {
			return test{
				ctx:          context.Background(),
				currentAdmin: &linkedca.Admin{Subject: "step"},
				otherAdmins: []*linkedca.Admin{
					{
						Subject: "otherAdmin",
					},
				},
				policy: &linkedca.Policy{
					X509: &linkedca.X509Policy{
						Allow: &linkedca.X509Names{
							Dns: []string{"step"},
						},
					},
				},
				err: &PolicyError{
					Typ: AdminLockOut,
					Err: errors.New("the provided policy would lock out [otherAdmin] from the CA. Please update your policy to include [otherAdmin] as an allowed name"),
				},
			}
		},
		"ok/no-policy": func(t *testing.T) test {
			return test{
				ctx:          context.Background(),
				currentAdmin: &linkedca.Admin{Subject: "step"},
				otherAdmins:  []*linkedca.Admin{},
				policy:       nil,
			}
		},
		"ok/empty-policy": func(t *testing.T) test {
			return test{
				ctx:          context.Background(),
				currentAdmin: &linkedca.Admin{Subject: "step"},
				otherAdmins:  []*linkedca.Admin{},
				policy: &linkedca.Policy{
					X509: &linkedca.X509Policy{
						Allow: &linkedca.X509Names{
							Dns: []string{},
						},
					},
				},
			}
		},
		"ok/policy": func(t *testing.T) test {
			return test{
				ctx:          context.Background(),
				currentAdmin: &linkedca.Admin{Subject: "step"},
				otherAdmins: []*linkedca.Admin{
					{
						Subject: "otherAdmin",
					},
				},
				policy: &linkedca.Policy{
					X509: &linkedca.X509Policy{
						Allow: &linkedca.X509Names{
							Dns: []string{"step", "otherAdmin"},
						},
					},
				},
			}
		},
	}

	for name, prep := range tests {
		tc := prep(t)
		t.Run(name, func(t *testing.T) {
			a := &Authority{}

			err := a.checkPolicy(tc.ctx, tc.currentAdmin, tc.otherAdmins, tc.policy)

			if tc.err == nil {
				assert.Nil(t, err)
			} else {
				assert.IsType(t, &PolicyError{}, err)

				pe, ok := err.(*PolicyError)
				assert.True(t, ok)

				assert.Equal(t, tc.err.Typ, pe.Typ)
				assert.Equal(t, tc.err.Error(), pe.Error())
			}
		})
	}
}

func Test_policyToCertificates(t *testing.T) {
	tests := []struct {
		name   string
		policy *linkedca.Policy
		want   *policy.Options
	}{
		{
			name:   "nil",
			policy: nil,
			want:   nil,
		},
		{
			name:   "no-policy",
			policy: &linkedca.Policy{},
			want:   nil,
		},
		{
			name: "partial-policy",
			policy: &linkedca.Policy{
				X509: &linkedca.X509Policy{
					Allow: &linkedca.X509Names{
						Dns: []string{"*.local"},
					},
					AllowWildcardLiteral:                 false,
					DisableSubjectCommonNameVerification: false,
				},
			},
			want: &policy.Options{
				X509: &policy.X509PolicyOptions{
					AllowedNames: &policy.X509NameOptions{
						DNSDomains: []string{"*.local"},
					},
					AllowWildcardLiteral:          false,
					DisableCommonNameVerification: false,
				},
			},
		},
		{
			name: "full-policy",
			policy: &linkedca.Policy{
				X509: &linkedca.X509Policy{
					Allow: &linkedca.X509Names{
						Dns:    []string{"step"},
						Ips:    []string{"127.0.0.1/24"},
						Emails: []string{"*.example.com"},
						Uris:   []string{"https://*.local"},
					},
					Deny: &linkedca.X509Names{
						Dns:    []string{"bad"},
						Ips:    []string{"127.0.0.30"},
						Emails: []string{"badhost.example.com"},
						Uris:   []string{"https://badhost.local"},
					},
					AllowWildcardLiteral:                 true,
					DisableSubjectCommonNameVerification: false,
				},
				Ssh: &linkedca.SSHPolicy{
					Host: &linkedca.SSHHostPolicy{
						Allow: &linkedca.SSHHostNames{
							Dns:        []string{"*.localhost"},
							Ips:        []string{"127.0.0.1/24"},
							Principals: []string{"user"},
						},
						Deny: &linkedca.SSHHostNames{
							Dns:        []string{"badhost.localhost"},
							Ips:        []string{"127.0.0.40"},
							Principals: []string{"root"},
						},
					},
					User: &linkedca.SSHUserPolicy{
						Allow: &linkedca.SSHUserNames{
							Emails:     []string{"@work"},
							Principals: []string{"user"},
						},
						Deny: &linkedca.SSHUserNames{
							Emails:     []string{"root@work"},
							Principals: []string{"root"},
						},
					},
				},
			},
			want: &policy.Options{
				X509: &policy.X509PolicyOptions{
					AllowedNames: &policy.X509NameOptions{
						DNSDomains:     []string{"step"},
						IPRanges:       []string{"127.0.0.1/24"},
						EmailAddresses: []string{"*.example.com"},
						URIDomains:     []string{"https://*.local"},
					},
					DeniedNames: &policy.X509NameOptions{
						DNSDomains:     []string{"bad"},
						IPRanges:       []string{"127.0.0.30"},
						EmailAddresses: []string{"badhost.example.com"},
						URIDomains:     []string{"https://badhost.local"},
					},
					AllowWildcardLiteral:          true,
					DisableCommonNameVerification: false,
				},
				SSH: &policy.SSHPolicyOptions{
					Host: &policy.SSHHostCertificateOptions{
						AllowedNames: &policy.SSHNameOptions{
							DNSDomains: []string{"*.localhost"},
							IPRanges:   []string{"127.0.0.1/24"},
							Principals: []string{"user"},
						},
						DeniedNames: &policy.SSHNameOptions{
							DNSDomains: []string{"badhost.localhost"},
							IPRanges:   []string{"127.0.0.40"},
							Principals: []string{"root"},
						},
					},
					User: &policy.SSHUserCertificateOptions{
						AllowedNames: &policy.SSHNameOptions{
							EmailAddresses: []string{"@work"},
							Principals:     []string{"user"},
						},
						DeniedNames: &policy.SSHNameOptions{
							EmailAddresses: []string{"root@work"},
							Principals:     []string{"root"},
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := policyToCertificates(tt.policy)
			if !cmp.Equal(tt.want, got) {
				t.Errorf("policyToCertificates() diff=\n%s", cmp.Diff(tt.want, got))
			}
		})
	}
}

func TestAuthority_reloadPolicyEngines(t *testing.T) {

	existingX509PolicyEngine, err := policy.NewX509PolicyEngine(&policy.X509PolicyOptions{
		AllowedNames: &policy.X509NameOptions{
			DNSDomains: []string{"*.hosts.example.com"},
		},
	})
	assert.NoError(t, err)

	existingSSHHostPolicyEngine, err := policy.NewSSHHostPolicyEngine(&policy.SSHPolicyOptions{
		Host: &policy.SSHHostCertificateOptions{
			AllowedNames: &policy.SSHNameOptions{
				DNSDomains: []string{"*.hosts.example.com"},
			},
		},
	})
	assert.NoError(t, err)

	existingSSHUserPolicyEngine, err := policy.NewSSHUserPolicyEngine(&policy.SSHPolicyOptions{
		User: &policy.SSHUserCertificateOptions{
			AllowedNames: &policy.SSHNameOptions{
				EmailAddresses: []string{"@mails.example.com"},
			},
		},
	})
	assert.NoError(t, err)

	newX509PolicyEngine, err := policy.NewX509PolicyEngine(&policy.X509PolicyOptions{
		AllowedNames: &policy.X509NameOptions{
			DNSDomains: []string{"*.local"},
		},
		DeniedNames: &policy.X509NameOptions{
			DNSDomains: []string{"badhost.local"},
		},
		AllowWildcardLiteral:          true,
		DisableCommonNameVerification: false,
	})
	assert.NoError(t, err)

	newSSHHostPolicyEngine, err := policy.NewSSHHostPolicyEngine(&policy.SSHPolicyOptions{
		Host: &policy.SSHHostCertificateOptions{
			AllowedNames: &policy.SSHNameOptions{
				DNSDomains: []string{"*.local"},
			},
			DeniedNames: &policy.SSHNameOptions{
				DNSDomains: []string{"badhost.local"},
			},
		},
	})
	assert.NoError(t, err)

	newSSHUserPolicyEngine, err := policy.NewSSHUserPolicyEngine(&policy.SSHPolicyOptions{
		User: &policy.SSHUserCertificateOptions{
			AllowedNames: &policy.SSHNameOptions{
				Principals: []string{"*"},
			},
			DeniedNames: &policy.SSHNameOptions{
				Principals: []string{"root"},
			},
		},
	})
	assert.NoError(t, err)

	newAdminX509PolicyEngine, err := policy.NewX509PolicyEngine(&policy.X509PolicyOptions{
		AllowedNames: &policy.X509NameOptions{
			DNSDomains: []string{"*.local"},
		},
	})
	assert.NoError(t, err)

	newAdminSSHHostPolicyEngine, err := policy.NewSSHHostPolicyEngine(&policy.SSHPolicyOptions{
		Host: &policy.SSHHostCertificateOptions{
			AllowedNames: &policy.SSHNameOptions{
				DNSDomains: []string{"*.local"},
			},
		},
	})
	assert.NoError(t, err)

	newAdminSSHUserPolicyEngine, err := policy.NewSSHUserPolicyEngine(&policy.SSHPolicyOptions{
		User: &policy.SSHUserCertificateOptions{
			AllowedNames: &policy.SSHNameOptions{
				EmailAddresses: []string{"@example.com"},
			},
		},
	})
	assert.NoError(t, err)

	type expected struct {
		x509Policy    policy.X509Policy
		sshUserPolicy policy.UserPolicy
		sshHostPolicy policy.HostPolicy
	}
	tests := []struct {
		name     string
		config   *config.Config
		adminDB  admin.DB
		ctx      context.Context
		expected *expected
		wantErr  bool
	}{
		{
			name: "fail/standalone-x509-policy",
			config: &config.Config{
				AuthorityConfig: &config.AuthConfig{
					EnableAdmin: false,
					Policy: &policy.Options{
						X509: &policy.X509PolicyOptions{
							AllowedNames: &policy.X509NameOptions{
								DNSDomains: []string{"**.local"},
							},
						},
					},
				},
			},
			ctx:     context.Background(),
			wantErr: true,
			expected: &expected{
				x509Policy:    existingX509PolicyEngine,
				sshUserPolicy: existingSSHUserPolicyEngine,
				sshHostPolicy: existingSSHHostPolicyEngine,
			},
		},
		{
			name: "fail/standalone-ssh-host-policy",
			config: &config.Config{
				AuthorityConfig: &config.AuthConfig{
					EnableAdmin: false,
					Policy: &policy.Options{
						SSH: &policy.SSHPolicyOptions{
							Host: &policy.SSHHostCertificateOptions{
								AllowedNames: &policy.SSHNameOptions{
									DNSDomains: []string{"**.local"},
								},
							},
						},
					},
				},
			},
			ctx:     context.Background(),
			wantErr: true,
			expected: &expected{
				x509Policy:    existingX509PolicyEngine,
				sshUserPolicy: existingSSHUserPolicyEngine,
				sshHostPolicy: existingSSHHostPolicyEngine,
			},
		},
		{
			name: "fail/standalone-ssh-user-policy",
			config: &config.Config{
				AuthorityConfig: &config.AuthConfig{
					EnableAdmin: false,
					Policy: &policy.Options{
						SSH: &policy.SSHPolicyOptions{
							User: &policy.SSHUserCertificateOptions{
								AllowedNames: &policy.SSHNameOptions{
									EmailAddresses: []string{"**example.com"},
								},
							},
						},
					},
				},
			},
			ctx:     context.Background(),
			wantErr: true,
			expected: &expected{
				x509Policy:    existingX509PolicyEngine,
				sshUserPolicy: existingSSHUserPolicyEngine,
				sshHostPolicy: existingSSHHostPolicyEngine,
			},
		},
		{
			name: "fail/adminDB.GetAuthorityPolicy-error",
			config: &config.Config{
				AuthorityConfig: &config.AuthConfig{
					EnableAdmin: true,
				},
			},
			adminDB: &admin.MockDB{
				MockGetAuthorityPolicy: func(ctx context.Context) (*linkedca.Policy, error) {
					return nil, errors.New("force")
				},
			},
			ctx:     context.Background(),
			wantErr: true,
			expected: &expected{
				x509Policy:    existingX509PolicyEngine,
				sshUserPolicy: existingSSHUserPolicyEngine,
				sshHostPolicy: existingSSHHostPolicyEngine,
			},
		},
		{
			name: "fail/admin-x509-policy",
			config: &config.Config{
				AuthorityConfig: &config.AuthConfig{
					EnableAdmin: true,
				},
			},
			adminDB: &admin.MockDB{
				MockGetAuthorityPolicy: func(ctx context.Context) (*linkedca.Policy, error) {
					return &linkedca.Policy{
						X509: &linkedca.X509Policy{
							Allow: &linkedca.X509Names{
								Dns: []string{"**.local"},
							},
						},
					}, nil
				},
			},
			ctx:     context.Background(),
			wantErr: true,
			expected: &expected{
				x509Policy:    existingX509PolicyEngine,
				sshUserPolicy: existingSSHUserPolicyEngine,
				sshHostPolicy: existingSSHHostPolicyEngine,
			},
		},
		{
			name: "fail/admin-ssh-host-policy",
			config: &config.Config{
				AuthorityConfig: &config.AuthConfig{
					EnableAdmin: true,
				},
			},
			adminDB: &admin.MockDB{
				MockGetAuthorityPolicy: func(ctx context.Context) (*linkedca.Policy, error) {
					return &linkedca.Policy{
						Ssh: &linkedca.SSHPolicy{
							Host: &linkedca.SSHHostPolicy{
								Allow: &linkedca.SSHHostNames{
									Dns: []string{"**.local"},
								},
							},
						},
					}, nil
				},
			},
			ctx:     context.Background(),
			wantErr: true,
			expected: &expected{
				x509Policy:    existingX509PolicyEngine,
				sshUserPolicy: existingSSHUserPolicyEngine,
				sshHostPolicy: existingSSHHostPolicyEngine,
			},
		},
		{
			name: "fail/admin-ssh-user-policy",
			config: &config.Config{
				AuthorityConfig: &config.AuthConfig{
					EnableAdmin: true,
				},
			},
			adminDB: &admin.MockDB{
				MockGetAuthorityPolicy: func(ctx context.Context) (*linkedca.Policy, error) {
					return &linkedca.Policy{
						Ssh: &linkedca.SSHPolicy{
							User: &linkedca.SSHUserPolicy{
								Allow: &linkedca.SSHUserNames{
									Emails: []string{"@@example.com"},
								},
							},
						},
					}, nil
				},
			},
			ctx:     context.Background(),
			wantErr: true,
			expected: &expected{
				x509Policy:    existingX509PolicyEngine,
				sshUserPolicy: existingSSHUserPolicyEngine,
				sshHostPolicy: existingSSHHostPolicyEngine,
			},
		},
		{
			name: "ok/linkedca-unsupported",
			config: &config.Config{
				AuthorityConfig: &config.AuthConfig{
					EnableAdmin: true,
				},
			},
			adminDB: &linkedCaClient{},
			ctx:     context.Background(),
			wantErr: false,
			expected: &expected{
				x509Policy:    existingX509PolicyEngine,
				sshUserPolicy: existingSSHUserPolicyEngine,
				sshHostPolicy: existingSSHHostPolicyEngine,
			},
		},
		{
			name: "ok/standalone-no-policy",
			config: &config.Config{
				AuthorityConfig: &config.AuthConfig{
					EnableAdmin: false,
					Policy:      nil,
				},
			},
			ctx:     context.Background(),
			wantErr: false,
			expected: &expected{
				x509Policy:    nil,
				sshUserPolicy: nil,
				sshHostPolicy: nil,
			},
		},
		{
			name: "ok/standalone-x509-policy",
			config: &config.Config{
				AuthorityConfig: &config.AuthConfig{
					EnableAdmin: false,
					Policy: &policy.Options{
						X509: &policy.X509PolicyOptions{
							AllowedNames: &policy.X509NameOptions{
								DNSDomains: []string{"*.local"},
							},
							DeniedNames: &policy.X509NameOptions{
								DNSDomains: []string{"badhost.local"},
							},
							AllowWildcardLiteral:          true,
							DisableCommonNameVerification: false,
						},
					},
				},
			},
			ctx:     context.Background(),
			wantErr: false,
			expected: &expected{
				// expect only the X.509 policy to exist
				x509Policy:    newX509PolicyEngine,
				sshHostPolicy: nil,
				sshUserPolicy: nil,
			},
		},
		{
			name: "ok/standalone-ssh-host-policy",
			config: &config.Config{
				AuthorityConfig: &config.AuthConfig{
					EnableAdmin: false,
					Policy: &policy.Options{
						SSH: &policy.SSHPolicyOptions{
							Host: &policy.SSHHostCertificateOptions{
								AllowedNames: &policy.SSHNameOptions{
									DNSDomains: []string{"*.local"},
								},
								DeniedNames: &policy.SSHNameOptions{
									DNSDomains: []string{"badhost.local"},
								},
							},
						},
					},
				},
			},
			ctx:     context.Background(),
			wantErr: false,
			expected: &expected{
				// expect only the SSH host policy to exist
				x509Policy:    nil,
				sshHostPolicy: newSSHHostPolicyEngine,
				sshUserPolicy: nil,
			},
		},
		{
			name: "ok/standalone-ssh-user-policy",
			config: &config.Config{
				AuthorityConfig: &config.AuthConfig{
					EnableAdmin: false,
					Policy: &policy.Options{
						SSH: &policy.SSHPolicyOptions{
							User: &policy.SSHUserCertificateOptions{
								AllowedNames: &policy.SSHNameOptions{
									Principals: []string{"*"},
								},
								DeniedNames: &policy.SSHNameOptions{
									Principals: []string{"root"},
								},
							},
						},
					},
				},
			},
			ctx:     context.Background(),
			wantErr: false,
			expected: &expected{
				// expect only the SSH user policy to exist
				x509Policy:    nil,
				sshHostPolicy: nil,
				sshUserPolicy: newSSHUserPolicyEngine,
			},
		},
		{
			name: "ok/standalone-ssh-policy",
			config: &config.Config{
				AuthorityConfig: &config.AuthConfig{
					EnableAdmin: false,
					Policy: &policy.Options{
						SSH: &policy.SSHPolicyOptions{
							Host: &policy.SSHHostCertificateOptions{
								AllowedNames: &policy.SSHNameOptions{
									DNSDomains: []string{"*.local"},
								},
								DeniedNames: &policy.SSHNameOptions{
									DNSDomains: []string{"badhost.local"},
								},
							},
							User: &policy.SSHUserCertificateOptions{
								AllowedNames: &policy.SSHNameOptions{
									Principals: []string{"*"},
								},
								DeniedNames: &policy.SSHNameOptions{
									Principals: []string{"root"},
								},
							},
						},
					},
				},
			},
			ctx:     context.Background(),
			wantErr: false,
			expected: &expected{
				// expect only the SSH policy engines to exist
				x509Policy:    nil,
				sshHostPolicy: newSSHHostPolicyEngine,
				sshUserPolicy: newSSHUserPolicyEngine,
			},
		},
		{
			name: "ok/standalone-full-policy",
			config: &config.Config{
				AuthorityConfig: &config.AuthConfig{
					EnableAdmin: false,
					Policy: &policy.Options{
						X509: &policy.X509PolicyOptions{
							AllowedNames: &policy.X509NameOptions{
								DNSDomains: []string{"*.local"},
							},
							DeniedNames: &policy.X509NameOptions{
								DNSDomains: []string{"badhost.local"},
							},
							AllowWildcardLiteral:          true,
							DisableCommonNameVerification: false,
						},
						SSH: &policy.SSHPolicyOptions{
							Host: &policy.SSHHostCertificateOptions{
								AllowedNames: &policy.SSHNameOptions{
									DNSDomains: []string{"*.local"},
								},
								DeniedNames: &policy.SSHNameOptions{
									DNSDomains: []string{"badhost.local"},
								},
							},
							User: &policy.SSHUserCertificateOptions{
								AllowedNames: &policy.SSHNameOptions{
									Principals: []string{"*"},
								},
								DeniedNames: &policy.SSHNameOptions{
									Principals: []string{"root"},
								},
							},
						},
					},
				},
			},
			ctx:     context.Background(),
			wantErr: false,
			expected: &expected{
				// expect all three policy engines to exist
				x509Policy:    newX509PolicyEngine,
				sshHostPolicy: newSSHHostPolicyEngine,
				sshUserPolicy: newSSHUserPolicyEngine,
			},
		},
		{
			name: "ok/admin-x509-policy",
			config: &config.Config{
				AuthorityConfig: &config.AuthConfig{
					EnableAdmin: true,
				},
			},
			adminDB: &admin.MockDB{
				MockGetAuthorityPolicy: func(ctx context.Context) (*linkedca.Policy, error) {
					return &linkedca.Policy{
						X509: &linkedca.X509Policy{
							Allow: &linkedca.X509Names{
								Dns: []string{"*.local"},
							},
						},
					}, nil
				},
			},
			ctx:     context.Background(),
			wantErr: false,
			expected: &expected{
				x509Policy:    newAdminX509PolicyEngine,
				sshHostPolicy: nil,
				sshUserPolicy: nil,
			},
		},
		{
			name: "ok/admin-ssh-host-policy",
			config: &config.Config{
				AuthorityConfig: &config.AuthConfig{
					EnableAdmin: true,
				},
			},
			adminDB: &admin.MockDB{
				MockGetAuthorityPolicy: func(ctx context.Context) (*linkedca.Policy, error) {
					return &linkedca.Policy{
						Ssh: &linkedca.SSHPolicy{
							Host: &linkedca.SSHHostPolicy{
								Allow: &linkedca.SSHHostNames{
									Dns: []string{"*.local"},
								},
							},
						},
					}, nil
				},
			},
			ctx:     context.Background(),
			wantErr: false,
			expected: &expected{
				x509Policy:    nil,
				sshHostPolicy: newAdminSSHHostPolicyEngine,
				sshUserPolicy: nil,
			},
		},
		{
			name: "ok/admin-ssh-user-policy",
			config: &config.Config{
				AuthorityConfig: &config.AuthConfig{
					EnableAdmin: true,
				},
			},
			adminDB: &admin.MockDB{
				MockGetAuthorityPolicy: func(ctx context.Context) (*linkedca.Policy, error) {
					return &linkedca.Policy{
						Ssh: &linkedca.SSHPolicy{
							User: &linkedca.SSHUserPolicy{
								Allow: &linkedca.SSHUserNames{
									Emails: []string{"@example.com"},
								},
							},
						},
					}, nil
				},
			},
			ctx:     context.Background(),
			wantErr: false,
			expected: &expected{
				x509Policy:    nil,
				sshHostPolicy: nil,
				sshUserPolicy: newAdminSSHUserPolicyEngine,
			},
		},
		{
			name: "ok/admin-full-policy",
			config: &config.Config{
				AuthorityConfig: &config.AuthConfig{
					EnableAdmin: true,
				},
			},
			ctx: context.Background(),
			adminDB: &admin.MockDB{
				MockGetAuthorityPolicy: func(ctx context.Context) (*linkedca.Policy, error) {
					return &linkedca.Policy{
						X509: &linkedca.X509Policy{
							Allow: &linkedca.X509Names{
								Dns: []string{"*.local"},
							},
							Deny: &linkedca.X509Names{
								Dns: []string{"badhost.local"},
							},
							AllowWildcardLiteral:                 true,
							DisableSubjectCommonNameVerification: false,
						},
						Ssh: &linkedca.SSHPolicy{
							Host: &linkedca.SSHHostPolicy{
								Allow: &linkedca.SSHHostNames{
									Dns: []string{"*.local"},
								},
							},
							User: &linkedca.SSHUserPolicy{
								Allow: &linkedca.SSHUserNames{
									Emails: []string{"@example.com"},
								},
							},
						},
					}, nil
				},
			},
			wantErr: false,
			expected: &expected{
				// expect all three policy engines to exist
				x509Policy:    newX509PolicyEngine,
				sshHostPolicy: newAdminSSHHostPolicyEngine,
				sshUserPolicy: newAdminSSHUserPolicyEngine,
			},
		},
		{
			// both DB and JSON config; DB config is taken if Admin API is enabled
			name: "ok/admin-over-standalone",
			config: &config.Config{
				AuthorityConfig: &config.AuthConfig{
					EnableAdmin: true,
					Policy: &policy.Options{
						SSH: &policy.SSHPolicyOptions{
							Host: &policy.SSHHostCertificateOptions{
								AllowedNames: &policy.SSHNameOptions{
									DNSDomains: []string{"*.local"},
								},
								DeniedNames: &policy.SSHNameOptions{
									DNSDomains: []string{"badhost.local"},
								},
							},
							User: &policy.SSHUserCertificateOptions{
								AllowedNames: &policy.SSHNameOptions{
									Principals: []string{"*"},
								},
								DeniedNames: &policy.SSHNameOptions{
									Principals: []string{"root"},
								},
							},
						},
					},
				},
			},
			ctx: context.Background(),
			adminDB: &admin.MockDB{
				MockGetAuthorityPolicy: func(ctx context.Context) (*linkedca.Policy, error) {
					return &linkedca.Policy{
						X509: &linkedca.X509Policy{
							Allow: &linkedca.X509Names{
								Dns: []string{"*.local"},
							},
							Deny: &linkedca.X509Names{
								Dns: []string{"badhost.local"},
							},
							AllowWildcardLiteral:                 true,
							DisableSubjectCommonNameVerification: false,
						},
					}, nil
				},
			},
			wantErr: false,
			expected: &expected{
				// expect all three policy engines to exist
				x509Policy:    newX509PolicyEngine,
				sshHostPolicy: nil,
				sshUserPolicy: nil,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Authority{
				config:        tt.config,
				adminDB:       tt.adminDB,
				x509Policy:    existingX509PolicyEngine,
				sshUserPolicy: existingSSHUserPolicyEngine,
				sshHostPolicy: existingSSHHostPolicyEngine,
			}
			if err := a.reloadPolicyEngines(tt.ctx); (err != nil) != tt.wantErr {
				t.Errorf("Authority.reloadPolicyEngines() error = %v, wantErr %v", err, tt.wantErr)
			}

			assert.Equal(t, tt.expected.x509Policy, a.x509Policy)
			assert.Equal(t, tt.expected.sshHostPolicy, a.sshHostPolicy)
			assert.Equal(t, tt.expected.sshUserPolicy, a.sshUserPolicy)
		})
	}
}

func TestAuthority_checkAuthorityPolicy(t *testing.T) {
	type fields struct {
		provisioners *provisioner.Collection
		admins       *administrator.Collection
		db           db.AuthDB
		adminDB      admin.DB
	}
	type args struct {
		ctx          context.Context
		currentAdmin *linkedca.Admin
		provName     string
		p            *linkedca.Policy
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name:   "no policy",
			fields: fields{},
			args: args{
				currentAdmin: nil,
				provName:     "prov",
				p:            nil,
			},
			wantErr: false,
		},
		{
			name: "fail/adminDB.GetAdmins-error",
			fields: fields{
				admins: administrator.NewCollection(nil),
				adminDB: &admin.MockDB{
					MockGetAdmins: func(ctx context.Context) ([]*linkedca.Admin, error) {
						return nil, errors.New("force")
					},
				},
			},
			args: args{
				currentAdmin: &linkedca.Admin{Subject: "step"},
				provName:     "prov",
				p: &linkedca.Policy{
					X509: &linkedca.X509Policy{
						Allow: &linkedca.X509Names{
							Dns: []string{"step", "otherAdmin"},
						},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "ok",
			fields: fields{
				admins: administrator.NewCollection(nil),
				adminDB: &admin.MockDB{
					MockGetAdmins: func(ctx context.Context) ([]*linkedca.Admin, error) {
						return []*linkedca.Admin{}, nil
					},
				},
			},
			args: args{
				currentAdmin: &linkedca.Admin{Subject: "step"},
				provName:     "prov",
				p: &linkedca.Policy{
					X509: &linkedca.X509Policy{
						Allow: &linkedca.X509Names{
							Dns: []string{"step", "otherAdmin"},
						},
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Authority{
				provisioners: tt.fields.provisioners,
				admins:       tt.fields.admins,
				db:           tt.fields.db,
				adminDB:      tt.fields.adminDB,
			}
			if err := a.checkAuthorityPolicy(tt.args.ctx, tt.args.currentAdmin, tt.args.p); (err != nil) != tt.wantErr {
				t.Errorf("Authority.checkProvisionerPolicy() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAuthority_checkProvisionerPolicy(t *testing.T) {
	type fields struct {
		provisioners *provisioner.Collection
		admins       *administrator.Collection
		db           db.AuthDB
		adminDB      admin.DB
	}
	type args struct {
		ctx          context.Context
		currentAdmin *linkedca.Admin
		provName     string
		p            *linkedca.Policy
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name:   "no policy",
			fields: fields{},
			args: args{
				currentAdmin: nil,
				provName:     "prov",
				p:            nil,
			},
			wantErr: false,
		},
		{
			name: "ok",
			fields: fields{
				admins: administrator.NewCollection(nil),
			},
			args: args{
				currentAdmin: &linkedca.Admin{Subject: "step"},
				provName:     "prov",
				p: &linkedca.Policy{
					X509: &linkedca.X509Policy{
						Allow: &linkedca.X509Names{
							Dns: []string{"step", "otherAdmin"},
						},
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Authority{
				provisioners: tt.fields.provisioners,
				admins:       tt.fields.admins,
				db:           tt.fields.db,
				adminDB:      tt.fields.adminDB,
			}
			if err := a.checkProvisionerPolicy(tt.args.ctx, tt.args.currentAdmin, tt.args.provName, tt.args.p); (err != nil) != tt.wantErr {
				t.Errorf("Authority.checkProvisionerPolicy() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAuthority_RemoveAuthorityPolicy(t *testing.T) {
	type fields struct {
		config  *config.Config
		db      db.AuthDB
		adminDB admin.DB
	}
	type args struct {
		ctx context.Context
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr *PolicyError
	}{
		{
			name: "fail/adminDB.DeleteAuthorityPolicy",
			fields: fields{
				config: &config.Config{
					AuthorityConfig: &config.AuthConfig{
						EnableAdmin: true,
					},
				},
				adminDB: &admin.MockDB{
					MockDeleteAuthorityPolicy: func(ctx context.Context) error {
						return errors.New("force")
					},
				},
			},
			wantErr: &PolicyError{
				Typ: StoreFailure,
				Err: errors.New("force"),
			},
		},
		{
			name: "fail/a.reloadPolicyEngines",
			fields: fields{
				config: &config.Config{
					AuthorityConfig: &config.AuthConfig{
						EnableAdmin: true,
					},
				},
				adminDB: &admin.MockDB{
					MockDeleteAuthorityPolicy: func(ctx context.Context) error {
						return nil
					},
					MockGetAuthorityPolicy: func(ctx context.Context) (*linkedca.Policy, error) {
						return nil, errors.New("force")
					},
				},
			},
			wantErr: &PolicyError{
				Typ: ReloadFailure,
				Err: errors.New("error reloading policy engines when deleting authority policy: error getting policy to (re)load policy engines: force"),
			},
		},
		{
			name: "ok",
			fields: fields{
				config: &config.Config{
					AuthorityConfig: &config.AuthConfig{
						EnableAdmin: true,
					},
				},
				adminDB: &admin.MockDB{
					MockDeleteAuthorityPolicy: func(ctx context.Context) error {
						return nil
					},
					MockGetAuthorityPolicy: func(ctx context.Context) (*linkedca.Policy, error) {
						return nil, nil
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Authority{
				config:  tt.fields.config,
				db:      tt.fields.db,
				adminDB: tt.fields.adminDB,
			}
			err := a.RemoveAuthorityPolicy(tt.args.ctx)
			if err != nil {
				pe, ok := err.(*PolicyError)
				assert.True(t, ok)
				assert.Equal(t, tt.wantErr.Typ, pe.Typ)
				assert.Equal(t, tt.wantErr.Err.Error(), pe.Err.Error())
				return
			}
		})
	}
}

func TestAuthority_GetAuthorityPolicy(t *testing.T) {
	type fields struct {
		config  *config.Config
		db      db.AuthDB
		adminDB admin.DB
	}
	type args struct {
		ctx context.Context
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *linkedca.Policy
		wantErr *PolicyError
	}{
		{
			name: "fail/adminDB.GetAuthorityPolicy",
			fields: fields{
				config: &config.Config{
					AuthorityConfig: &config.AuthConfig{
						EnableAdmin: true,
					},
				},
				adminDB: &admin.MockDB{
					MockGetAuthorityPolicy: func(ctx context.Context) (*linkedca.Policy, error) {
						return nil, errors.New("force")
					},
				},
			},
			wantErr: &PolicyError{
				Typ: InternalFailure,
				Err: errors.New("force"),
			},
		},
		{
			name: "ok",
			fields: fields{
				config: &config.Config{
					AuthorityConfig: &config.AuthConfig{
						EnableAdmin: true,
					},
				},
				adminDB: &admin.MockDB{
					MockGetAuthorityPolicy: func(ctx context.Context) (*linkedca.Policy, error) {
						return &linkedca.Policy{}, nil
					},
				},
			},
			want: &linkedca.Policy{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Authority{
				config:  tt.fields.config,
				db:      tt.fields.db,
				adminDB: tt.fields.adminDB,
			}
			got, err := a.GetAuthorityPolicy(tt.args.ctx)
			if err != nil {
				pe, ok := err.(*PolicyError)
				assert.True(t, ok)
				assert.Equal(t, tt.wantErr.Typ, pe.Typ)
				assert.Equal(t, tt.wantErr.Err.Error(), pe.Err.Error())
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Authority.GetAuthorityPolicy() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAuthority_CreateAuthorityPolicy(t *testing.T) {
	type fields struct {
		config  *config.Config
		db      db.AuthDB
		adminDB admin.DB
	}
	type args struct {
		ctx context.Context
		adm *linkedca.Admin
		p   *linkedca.Policy
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *linkedca.Policy
		wantErr *PolicyError
	}{
		{
			name: "fail/a.checkAuthorityPolicy",
			fields: fields{
				config: &config.Config{
					AuthorityConfig: &config.AuthConfig{
						EnableAdmin: true,
					},
				},
				adminDB: &admin.MockDB{
					MockGetAdmins: func(ctx context.Context) ([]*linkedca.Admin, error) {
						return nil, errors.New("force")
					},
				},
			},
			args: args{
				ctx: context.Background(),
				adm: &linkedca.Admin{Subject: "step"},
				p: &linkedca.Policy{
					X509: &linkedca.X509Policy{
						Allow: &linkedca.X509Names{
							Dns: []string{"step", "otherAdmin"},
						},
					},
				},
			},
			wantErr: &PolicyError{
				Typ: InternalFailure,
				Err: errors.New("error retrieving admins: force"),
			},
		},
		{
			name: "fail/adminDB.CreateAuthorityPolicy",
			fields: fields{
				config: &config.Config{
					AuthorityConfig: &config.AuthConfig{
						EnableAdmin: true,
					},
				},
				adminDB: &admin.MockDB{
					MockGetAdmins: func(ctx context.Context) ([]*linkedca.Admin, error) {
						return []*linkedca.Admin{}, nil
					},
					MockCreateAuthorityPolicy: func(ctx context.Context, policy *linkedca.Policy) error {
						return errors.New("force")
					},
				},
			},
			args: args{
				ctx: context.Background(),
				adm: &linkedca.Admin{Subject: "step"},
				p: &linkedca.Policy{
					X509: &linkedca.X509Policy{
						Allow: &linkedca.X509Names{
							Dns: []string{"step", "otherAdmin"},
						},
					},
				},
			},
			wantErr: &PolicyError{
				Typ: StoreFailure,
				Err: errors.New("force"),
			},
		},
		{
			name: "fail/a.reloadPolicyEngines",
			fields: fields{
				config: &config.Config{
					AuthorityConfig: &config.AuthConfig{
						EnableAdmin: true,
					},
				},
				adminDB: &admin.MockDB{
					MockGetAuthorityPolicy: func(ctx context.Context) (*linkedca.Policy, error) {
						return nil, errors.New("force")
					},
					MockGetAdmins: func(ctx context.Context) ([]*linkedca.Admin, error) {
						return []*linkedca.Admin{}, nil
					},
				},
			},
			args: args{
				ctx: context.Background(),
				adm: &linkedca.Admin{Subject: "step"},
				p: &linkedca.Policy{
					X509: &linkedca.X509Policy{
						Allow: &linkedca.X509Names{
							Dns: []string{"step", "otherAdmin"},
						},
					},
				},
			},
			wantErr: &PolicyError{
				Typ: ReloadFailure,
				Err: errors.New("error reloading policy engines when creating authority policy: error getting policy to (re)load policy engines: force"),
			},
		},
		{
			name: "ok",
			fields: fields{
				config: &config.Config{
					AuthorityConfig: &config.AuthConfig{
						EnableAdmin: true,
					},
				},
				adminDB: &admin.MockDB{
					MockGetAuthorityPolicy: func(ctx context.Context) (*linkedca.Policy, error) {
						return &linkedca.Policy{
							X509: &linkedca.X509Policy{
								Allow: &linkedca.X509Names{
									Dns: []string{"step", "otherAdmin"},
								},
							},
						}, nil
					},
					MockGetAdmins: func(ctx context.Context) ([]*linkedca.Admin, error) {
						return []*linkedca.Admin{}, nil
					},
				},
			},
			args: args{
				ctx: context.Background(),
				adm: &linkedca.Admin{Subject: "step"},
				p: &linkedca.Policy{
					X509: &linkedca.X509Policy{
						Allow: &linkedca.X509Names{
							Dns: []string{"step", "otherAdmin"},
						},
					},
				},
			},
			want: &linkedca.Policy{
				X509: &linkedca.X509Policy{
					Allow: &linkedca.X509Names{
						Dns: []string{"step", "otherAdmin"},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Authority{
				config:  tt.fields.config,
				db:      tt.fields.db,
				adminDB: tt.fields.adminDB,
			}
			got, err := a.CreateAuthorityPolicy(tt.args.ctx, tt.args.adm, tt.args.p)
			if err != nil {
				pe, ok := err.(*PolicyError)
				assert.True(t, ok)
				assert.Equal(t, tt.wantErr.Typ, pe.Typ)
				assert.Equal(t, tt.wantErr.Err.Error(), pe.Err.Error())
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Authority.CreateAuthorityPolicy() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAuthority_UpdateAuthorityPolicy(t *testing.T) {
	type fields struct {
		config  *config.Config
		db      db.AuthDB
		adminDB admin.DB
	}
	type args struct {
		ctx context.Context
		adm *linkedca.Admin
		p   *linkedca.Policy
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *linkedca.Policy
		wantErr *PolicyError
	}{
		{
			name: "fail/a.checkAuthorityPolicy",
			fields: fields{
				config: &config.Config{
					AuthorityConfig: &config.AuthConfig{
						EnableAdmin: true,
					},
				},
				adminDB: &admin.MockDB{
					MockGetAdmins: func(ctx context.Context) ([]*linkedca.Admin, error) {
						return nil, errors.New("force")
					},
				},
			},
			args: args{
				ctx: context.Background(),
				adm: &linkedca.Admin{Subject: "step"},
				p: &linkedca.Policy{
					X509: &linkedca.X509Policy{
						Allow: &linkedca.X509Names{
							Dns: []string{"step", "otherAdmin"},
						},
					},
				},
			},
			wantErr: &PolicyError{
				Typ: InternalFailure,
				Err: errors.New("error retrieving admins: force"),
			},
		},
		{
			name: "fail/adminDB.UpdateAuthorityPolicy",
			fields: fields{
				config: &config.Config{
					AuthorityConfig: &config.AuthConfig{
						EnableAdmin: true,
					},
				},
				adminDB: &admin.MockDB{
					MockGetAdmins: func(ctx context.Context) ([]*linkedca.Admin, error) {
						return []*linkedca.Admin{}, nil
					},
					MockUpdateAuthorityPolicy: func(ctx context.Context, policy *linkedca.Policy) error {
						return errors.New("force")
					},
				},
			},
			args: args{
				ctx: context.Background(),
				adm: &linkedca.Admin{Subject: "step"},
				p: &linkedca.Policy{
					X509: &linkedca.X509Policy{
						Allow: &linkedca.X509Names{
							Dns: []string{"step", "otherAdmin"},
						},
					},
				},
			},
			wantErr: &PolicyError{
				Typ: StoreFailure,
				Err: errors.New("force"),
			},
		},
		{
			name: "fail/a.reloadPolicyEngines",
			fields: fields{
				config: &config.Config{
					AuthorityConfig: &config.AuthConfig{
						EnableAdmin: true,
					},
				},
				adminDB: &admin.MockDB{
					MockGetAuthorityPolicy: func(ctx context.Context) (*linkedca.Policy, error) {
						return nil, errors.New("force")
					},
					MockGetAdmins: func(ctx context.Context) ([]*linkedca.Admin, error) {
						return []*linkedca.Admin{}, nil
					},
				},
			},
			args: args{
				ctx: context.Background(),
				adm: &linkedca.Admin{Subject: "step"},
				p: &linkedca.Policy{
					X509: &linkedca.X509Policy{
						Allow: &linkedca.X509Names{
							Dns: []string{"step", "otherAdmin"},
						},
					},
				},
			},
			wantErr: &PolicyError{
				Typ: ReloadFailure,
				Err: errors.New("error reloading policy engines when updating authority policy: error getting policy to (re)load policy engines: force"),
			},
		},
		{
			name: "ok",
			fields: fields{
				config: &config.Config{
					AuthorityConfig: &config.AuthConfig{
						EnableAdmin: true,
					},
				},
				adminDB: &admin.MockDB{
					MockGetAuthorityPolicy: func(ctx context.Context) (*linkedca.Policy, error) {
						return &linkedca.Policy{
							X509: &linkedca.X509Policy{
								Allow: &linkedca.X509Names{
									Dns: []string{"step", "otherAdmin"},
								},
							},
						}, nil
					},
					MockUpdateAuthorityPolicy: func(ctx context.Context, policy *linkedca.Policy) error {
						return nil
					},
					MockGetAdmins: func(ctx context.Context) ([]*linkedca.Admin, error) {
						return []*linkedca.Admin{}, nil
					},
				},
			},
			args: args{
				ctx: context.Background(),
				adm: &linkedca.Admin{Subject: "step"},
				p: &linkedca.Policy{
					X509: &linkedca.X509Policy{
						Allow: &linkedca.X509Names{
							Dns: []string{"step", "otherAdmin"},
						},
					},
				},
			},
			want: &linkedca.Policy{
				X509: &linkedca.X509Policy{
					Allow: &linkedca.X509Names{
						Dns: []string{"step", "otherAdmin"},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &Authority{
				config:  tt.fields.config,
				db:      tt.fields.db,
				adminDB: tt.fields.adminDB,
			}
			got, err := a.UpdateAuthorityPolicy(tt.args.ctx, tt.args.adm, tt.args.p)
			if err != nil {
				pe, ok := err.(*PolicyError)
				assert.True(t, ok)
				assert.Equal(t, tt.wantErr.Typ, pe.Typ)
				assert.Equal(t, tt.wantErr.Err.Error(), pe.Err.Error())
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Authority.UpdateAuthorityPolicy() = %v, want %v", got, tt.want)
			}
		})
	}
}
