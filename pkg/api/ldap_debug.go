package api

import (
	"fmt"
	"net/http"

	"github.com/grafana/grafana/pkg/bus"
	"github.com/grafana/grafana/pkg/infra/log"
	"github.com/grafana/grafana/pkg/login"
	"github.com/grafana/grafana/pkg/models"
	"github.com/grafana/grafana/pkg/services/ldap"
	"github.com/grafana/grafana/pkg/services/multildap"
	"github.com/grafana/grafana/pkg/setting"
	"github.com/grafana/grafana/pkg/util"
)

var (
	getLDAPConfig = multildap.GetConfig
	newLDAP       = multildap.New

	logger = log.New("LDAP.debug")

	errOrganizationNotFound = func(orgId int64) error {
		return fmt.Errorf("Unable to find organization with ID '%d'", orgId)
	}
)

// LDAPAttribute is a serializer for user attributes mapped from LDAP. Is meant to display both the serialized value and the LDAP key we received it from.
type LDAPAttribute struct {
	ConfigAttributeValue string `json:"cfgAttrValue"`
	LDAPAttributeValue   string `json:"ldapValue"`
}

// RoleDTO is a serializer for mapped roles from LDAP
type RoleDTO struct {
	OrgId   int64           `json:"orgId"`
	OrgName string          `json:"orgName"`
	OrgRole models.RoleType `json:"orgRole"`
	GroupDN string          `json:"groupDN"`
}

// LDAPUserDTO is a serializer for users mapped from LDAP
type LDAPUserDTO struct {
	Name           *LDAPAttribute           `json:"name"`
	Surname        *LDAPAttribute           `json:"surname"`
	Email          *LDAPAttribute           `json:"email"`
	Username       *LDAPAttribute           `json:"login"`
	IsGrafanaAdmin *bool                    `json:"isGrafanaAdmin"`
	IsDisabled     bool                     `json:"isDisabled"`
	OrgRoles       []RoleDTO                `json:"roles"`
	Teams          []models.TeamOrgGroupDTO `json:"teams"`
}

// FetchOrgs fetches the organization(s) information by executing a single query to the database. Then, populating the DTO with the information retrieved.
func (user *LDAPUserDTO) FetchOrgs() error {
	orgIds := []int64{}

	for _, or := range user.OrgRoles {
		orgIds = append(orgIds, or.OrgId)
	}

	q := &models.SearchOrgsQuery{}
	q.Ids = orgIds

	if err := bus.Dispatch(q); err != nil {
		return err
	}

	orgNamesById := map[int64]string{}
	for _, org := range q.Result {
		orgNamesById[org.Id] = org.Name
	}

	for i, orgDTO := range user.OrgRoles {
		orgName := orgNamesById[orgDTO.OrgId]

		if orgName != "" {
			user.OrgRoles[i].OrgName = orgName
		} else {
			return errOrganizationNotFound(orgDTO.OrgId)
		}
	}

	return nil
}

// LDAPServerDTO is a serializer for LDAP server statuses
type LDAPServerDTO struct {
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Available bool   `json:"available"`
	Error     string `json:"error"`
}

// ReloadLDAPCfg reloads the LDAP configuration
func (server *HTTPServer) ReloadLDAPCfg() Response {
	if !ldap.IsEnabled() {
		return Error(http.StatusBadRequest, "LDAP is not enabled", nil)
	}

	err := ldap.ReloadConfig()
	if err != nil {
		return Error(http.StatusInternalServerError, "Failed to reload ldap config.", err)
	}
	return Success("LDAP config reloaded")
}

// GetLDAPStatus attempts to connect to all the configured LDAP servers and returns information on whenever they're availabe or not.
func (server *HTTPServer) GetLDAPStatus(c *models.ReqContext) Response {
	if !ldap.IsEnabled() {
		return Error(http.StatusBadRequest, "LDAP is not enabled", nil)
	}

	ldapConfig, err := getLDAPConfig()

	if err != nil {
		return Error(http.StatusBadRequest, "Failed to obtain the LDAP configuration. Please verify the configuration and try again.", err)
	}

	ldap := newLDAP(ldapConfig.Servers)

	statuses, err := ldap.Ping()

	if err != nil {
		return Error(http.StatusBadRequest, "Failed to connect to the LDAP server(s)", err)
	}

	serverDTOs := []*LDAPServerDTO{}
	for _, status := range statuses {
		s := &LDAPServerDTO{
			Host:      status.Host,
			Available: status.Available,
			Port:      status.Port,
		}

		if status.Error != nil {
			s.Error = status.Error.Error()
		}

		serverDTOs = append(serverDTOs, s)
	}

	return JSON(http.StatusOK, serverDTOs)
}

func (server *HTTPServer) PostSyncUserWithLDAP(c *models.ReqContext) Response {
	if !ldap.IsEnabled() {
		return Error(http.StatusBadRequest, "LDAP is not enabled", nil)
	}

	ldapConfig, err := getLDAPConfig()

	if err != nil {
		return Error(http.StatusBadRequest, "Failed to obtain the LDAP configuration. Please verify the configuration and try again.", err)
	}

	userId := c.ParamsInt64(":id")

	query := models.GetUserByIdQuery{Id: userId}

	err = bus.Dispatch(query)

	if err := bus.Dispatch(&query); err != nil {
		if err == models.ErrUserNotFound {
			return Error(404, models.ErrUserNotFound.Error(), nil)
		}

		return Error(500, "Failed to get user", err)
	}

	// Check for users only from LDAP

	ldapServer := newLDAP(ldapConfig.Servers)
	user, _, err := ldapServer.User(query.Result.Login)

	if err != nil {
		if err == ldap.ErrCouldNotFindUser { // User was not in the LDAP server - we need to take action:

			if setting.AdminUser == query.Result.Login { // User is *the* Grafana Admin. We cannot disable it.
				errMsg := fmt.Sprintf(`Refusing to sync grafana super admin "%s" - it would be disabled`, query.Result.Login)
				logger.Error(errMsg)
				return Error(http.StatusBadRequest, errMsg, err)
			}

			// Since the user was not in the LDAP server. Let's disable it.
			login.DisableExternalUser(query.Result.Login)
			return JSON(http.StatusOK, "User disabled without any updates in the information") // should this be a success?
		}
	}

	upsertCmd := &models.UpsertUserCommand{
		ExternalUser:  user,
		SignupAllowed: setting.LDAPAllowSignup,
	}

	err = bus.Dispatch(upsertCmd)

	if err != nil {
		return Error(http.StatusInternalServerError, "Failed to udpate the user", err)
	}

	return JSON(http.StatusOK, "user synced")
}

// GetUserFromLDAP finds an user based on a username in LDAP. This helps illustrate how would the particular user be mapped in Grafana when synced.
func (server *HTTPServer) GetUserFromLDAP(c *models.ReqContext) Response {
	if !ldap.IsEnabled() {
		return Error(http.StatusBadRequest, "LDAP is not enabled", nil)
	}

	ldapConfig, err := getLDAPConfig()

	if err != nil {
		return Error(http.StatusBadRequest, "Failed to obtain the LDAP configuration", err)
	}

	ldap := newLDAP(ldapConfig.Servers)

	username := c.Params(":username")

	if len(username) == 0 {
		return Error(http.StatusBadRequest, "Validation error. You must specify an username", nil)
	}

	user, serverConfig, err := ldap.User(username)

	if user == nil {
		return Error(http.StatusNotFound, "No user was found on the LDAP server(s)", err)
	}

	logger.Debug("user found", "user", user)

	name, surname := splitName(user.Name)

	u := &LDAPUserDTO{
		Name:           &LDAPAttribute{serverConfig.Attr.Name, name},
		Surname:        &LDAPAttribute{serverConfig.Attr.Surname, surname},
		Email:          &LDAPAttribute{serverConfig.Attr.Email, user.Email},
		Username:       &LDAPAttribute{serverConfig.Attr.Username, user.Login},
		IsGrafanaAdmin: user.IsGrafanaAdmin,
		IsDisabled:     user.IsDisabled,
	}

	orgRoles := []RoleDTO{}

	for _, g := range serverConfig.Groups {
		role := &RoleDTO{}

		if isMatchToLDAPGroup(user, g) {
			role.OrgId = g.OrgID
			role.OrgRole = user.OrgRoles[g.OrgID]
			role.GroupDN = g.GroupDN

			orgRoles = append(orgRoles, *role)
		} else {
			role.OrgId = g.OrgID
			role.GroupDN = g.GroupDN

			orgRoles = append(orgRoles, *role)
		}
	}

	u.OrgRoles = orgRoles

	logger.Debug("mapping org roles", "orgsRoles", u.OrgRoles)
	err = u.FetchOrgs()

	if err != nil {
		return Error(http.StatusBadRequest, "An oganization was not found - Please verify your LDAP configuration", err)
	}

	cmd := &models.GetTeamsForLDAPGroupCommand{Groups: user.Groups}
	err = bus.Dispatch(cmd)

	if err != bus.ErrHandlerNotFound && err != nil {
		return Error(http.StatusBadRequest, "Unable to find the teams for this user", err)
	}

	u.Teams = cmd.Result

	return JSON(200, u)
}

// isMatchToLDAPGroup determines if we were able to match an LDAP group to an organization+role.
// Since we allow one role per organization. If it's set, we were able to match it.
func isMatchToLDAPGroup(user *models.ExternalUserInfo, groupConfig *ldap.GroupToOrgRole) bool {
	return user.OrgRoles[groupConfig.OrgID] == groupConfig.OrgRole
}

// splitName receives the full name of a user and splits it into two parts: A name and a surname.
func splitName(name string) (string, string) {
	names := util.SplitString(name)

	switch len(names) {
	case 0:
		return "", ""
	case 1:
		return names[0], ""
	default:
		return names[0], names[1]
	}
}
