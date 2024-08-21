package jumpcloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	jcapiv1 "github.com/TheJumpCloud/jcapi-go/v1"
	jcapiv2 "github.com/TheJumpCloud/jcapi-go/v2"
	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"
)

func resourceUserGroup() *schema.Resource {
	return &schema.Resource{
		Create: resourceUserGroupCreate,
		Read:   resourceUserGroupRead,
		Update: resourceUserGroupUpdate,
		Delete: resourceUserGroupDelete,
		Schema: map[string]*schema.Schema{
			"name": {
				Type:     schema.TypeString,
				Required: true,
			},
			"attributes": {
				Type:     schema.TypeMap,
				Optional: true,
				Computed: true,
				ForceNew: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"posix_groups": {
							Type: schema.TypeString,
							// PosixGroups cannot be edited after group creation.
							ForceNew: true,
							Optional: true,
						},
						// enable_samba has a more complicated lifecycle,
						// Commenting out for now as it is ignored in CRU by the JCAPI
						// From Jumpcloud UI:
						// Samba Authentication must be configured in the
						// JumpCloud LDAP Directory and LDAP sync must be enabled
						// on this group before Samba Authentication can be enabled.
						// "enable_samba": {
						// 	Type:     schema.TypeBool,
						// 	Optional: true,
						// },
					},
				},
			},
			"members": {
				Type:        schema.TypeList,
				Optional:    true,
				Description: "This is a set of user emails associated with this group",
				Elem: &schema.Schema{
					Type: schema.TypeString,
				},
			},
		},
		Importer: &schema.ResourceImporter{
			State: schema.ImportStatePassthrough,
		},
	}
}

func resourceUserGroupCreate(d *schema.ResourceData, m interface{}) error {
	config := m.(*jcapiv2.Configuration)
	client := jcapiv2.NewAPIClient(config)

	body := jcapiv2.UserGroupPost{Name: d.Get("name").(string)}

	// For Attributes.PosixGroups, only the first member of the slice
	// is considered by the JCAPI
	if attr, ok := expandAttributes(d.Get("attributes")); ok {
		body.Attributes = attr
	}

	req := map[string]interface{}{
		"body": body,
	}
	group, res, err := client.UserGroupsApi.GroupsUserPost(context.TODO(),
		"", headerAccept, req)
	if err != nil {
		// TODO: sort out error essentials
		return fmt.Errorf("error creating user group %s: %s - response = %+v",
			(req["body"].(jcapiv2.UserGroupPost)).Name, err, res)
	}

	d.SetId(group.Id)

	memberIds, err := userEmailsToIDs(config, d.Get("members").([]interface{}))
	if err != nil {
		return err
	}

	for _, memberId := range memberIds {
		err := manageGroupMember(client, d, memberId, "add")
		if err != nil {
			return err
		}
	}
	return resourceUserGroupRead(d, m)
}

// resourceUserGroupRead uses a helper function that consumes the
// JC's HTTP API directly; the groups' attributes need to be kept in state
// as they are required for resourceUserGroupUpdate and the current
// implementation of the JC SDK doesn't support their retrieval
func resourceUserGroupRead(d *schema.ResourceData, m interface{}) error {
	config := m.(*jcapiv2.Configuration)

	group, ok, err := userGroupReadHelper(config, d.Id())
	if err != nil {
		return err
	}

	if !ok {
		// not found
		d.SetId("")
		return nil
	}

	d.SetId(group.ID)
	if err := d.Set("name", group.Name); err != nil {
		return err
	}
	if err := d.Set("attributes", flattenAttributes(&group.Attributes)); err != nil {
		return err
	}

	client := jcapiv2.NewAPIClient(config)
	memberIDs, err := getUserGroupMemberIDs(client, d.Id())
	if err != nil {
		return err
	}
	memberEmails, err := userIDsToEmails(config, memberIDs)
	if err != nil {
		return err
	}
	if err := d.Set("members", memberEmails); err != nil {
		return err
	}

	return nil
}

func userGroupReadHelper(config *jcapiv2.Configuration, id string) (ug *UserGroup,
	ok bool, err error) {

	req, err := http.NewRequest(http.MethodGet,
		config.BasePath+"/usergroups/"+id, nil)
	if err != nil {
		return
	}

	req.Header.Add("x-api-key", config.DefaultHeader["x-api-key"])
	if config.DefaultHeader["x-org-id"] != "" {
		req.Header.Add("x-org-id", config.DefaultHeader["x-org-id"])
	}
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Accept", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusNotFound {
		return
	}

	ok = true
	err = json.NewDecoder(res.Body).Decode(&ug)
	return
}

func resourceUserGroupUpdate(d *schema.ResourceData, m interface{}) error {
	config := m.(*jcapiv2.Configuration)
	client := jcapiv2.NewAPIClient(config)

	body := jcapiv2.UserGroupPost{Name: d.Get("name").(string)}
	if attr, ok := expandAttributes(d.Get("attributes")); ok {
		body.Attributes = attr
	} else {
		return errors.New("unable to update, attributes not expandable")
	}

	req := map[string]interface{}{
		"body": body,
	}
	// behaves like PUT, will fail if
	// attributes.posixGroups isn't sent, see GODOC
	_, res, err := client.UserGroupsApi.GroupsUserPatch(context.TODO(),
		d.Id(), "", headerAccept, req)
	if err != nil {
		// TODO: sort out error essentials
		return fmt.Errorf("error deleting user group:%s; response = %+v", err, res)
	}

	oldMemberIDs, err := getUserGroupMemberIDs(client, d.Id())
	if err != nil {
		return err
	}

	newMemberIDs, err := userEmailsToIDs(config, d.Get("members").([]interface{}))
	if err != nil {
		return err
	}

	//add any new users
	for _, newMemberID := range newMemberIDs {
		if !slices.Contains(oldMemberIDs, newMemberID) {
			err := manageGroupMember(client, d, newMemberID, "add")
			if err != nil {
				return err
			}
		}
	}

	//remove any old users
	for _, oldMemberID := range oldMemberIDs {
		if !slices.Contains(newMemberIDs, oldMemberID) {
			err := manageGroupMember(client, d, oldMemberID, "remove")
			if err != nil {
				return err
			}
		}
	}

	return resourceUserGroupRead(d, m)
}

func resourceUserGroupDelete(d *schema.ResourceData, m interface{}) error {
	config := m.(*jcapiv2.Configuration)
	client := jcapiv2.NewAPIClient(config)

	res, err := client.UserGroupsApi.GroupsUserDelete(context.TODO(),
		d.Id(), "", headerAccept, nil)
	if err != nil {
		// TODO: sort out error essentials
		return fmt.Errorf("error deleting user group:%s; response = %+v", err, res)
	}
	d.SetId("")
	return nil
}

func getUserGroupMemberIDs(client *jcapiv2.APIClient, groupID string) ([]string, error) {
	var userIds []string
	for i := 0; ; i++ {
		optionals := map[string]interface{}{
			"groupId": groupID,
			"limit":   int32(100),
			"skip":    int32(i * 100),
		}

		graphconnect, res, err := client.UserGroupMembersMembershipApi.GraphUserGroupMembersList(
			context.TODO(), groupID, "", "", optionals)
		if err != nil {
			return nil, err
			return nil, fmt.Errorf("error group members for group id %s, error:%s; response = %+v", groupID, err, res)
		}

		for _, v := range graphconnect {
			userIds = append(userIds, v.To.Id)
		}

		if len(graphconnect) < 100 {
			break
		} else {
			time.Sleep(100 * time.Millisecond)
		}
	}
	return userIds, nil
}

func userIDsToEmails(configv2 *jcapiv2.Configuration, userIDs []string) ([]string, error) {
	var emails []string

	if len(userIDs) == 0 {
		return emails, nil
	}

	configv1 := convertV2toV1Config(configv2)
	client := jcapiv1.NewAPIClient(configv1)

	for i := 0; ; i++ {
		users, res, err := client.SystemusersApi.SystemusersList(context.TODO(), "", "", map[string]interface{}{
			"filter": "_id:$in:" + strings.Join(userIDs[:], "|"),
			"limit":  int32(100),
			"skip":   int32(i * 100),
			"fields": "email",
			"sort":   "email",
		})

		if err != nil {
			return nil, fmt.Errorf("error loading user emails from IDs: %s, i:%d, error:%s; response:%+v", userIDs, i, err, res)
		}

		for _, result := range users.Results {
			emails = append(emails, result.Email)
		}

		if len(users.Results) < 100 {
			break
		} else {
			time.Sleep(100 * time.Millisecond)
		}
	}

	return emails, nil
}

func userEmailsToIDs(configv2 *jcapiv2.Configuration, userEmailsInterface []interface{}) ([]string, error) {
	var userEmails []string
	for _, userEmail := range userEmailsInterface {
		userEmails = append(userEmails, userEmail.(string))
	}

	var ids []string

	if len(userEmails) == 0 {
		return ids, nil
	}

	configv1 := convertV2toV1Config(configv2)
	client := jcapiv1.NewAPIClient(configv1)

	for i := 0; ; i++ {
		users, res, err := client.SystemusersApi.SystemusersList(context.TODO(), "", "", map[string]interface{}{
			"filter": "email:$in:" + strings.Join(userEmails[:], "|"),
			"limit":  int32(100),
			"skip":   int32(i * 100),
			"fields": "_id",
			"sort":   "_id",
		})

		if err != nil {
			return nil, fmt.Errorf("error loading user IDs from emails:%s; response = %+v", err, res)
		}

		for _, result := range users.Results {
			ids = append(ids, result.Id)
		}

		if len(users.Results) < 100 {
			break
		} else {
			time.Sleep(100 * time.Millisecond)
		}
	}

	return ids, nil
}

func manageGroupMember(client *jcapiv2.APIClient, d *schema.ResourceData, memberID string, action string) error {
	payload := jcapiv2.UserGroupMembersReq{
		Op:    action,
		Type_: "user",
		Id:    memberID,
	}

	req := map[string]interface{}{
		"body": payload,
	}

	res, err := client.UserGroupMembersMembershipApi.GraphUserGroupMembersPost(
		context.TODO(), d.Id(), "", "", req)

	if err != nil {
		return fmt.Errorf("error managing group member, action: %s, member id:%s, error: %s; response = %+v", action, memberID, err, res)
	}
	return nil
}
