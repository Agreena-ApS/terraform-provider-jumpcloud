package jumpcloud

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"
    "time"

	jcapiv2 "github.com/TheJumpCloud/jcapi-go/v2"
	"github.com/hashicorp/terraform-plugin-sdk/helper/acctest"
	"github.com/hashicorp/terraform-plugin-sdk/helper/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

func TestAccUserGroup(t *testing.T) {
	rName := acctest.RandStringFromCharSet(10, acctest.CharSetAlpha)
	posixName := acctest.RandStringFromCharSet(10, acctest.CharSetAlpha)
	gid := acctest.RandIntRange(1, 1000)

	emails := make([]string, 123)
	for i := 0; i < 123; i++ {
		emails[i] = fmt.Sprintf("%s%d@testorg.com", rName, i)
	}
	sort.Strings(emails)

	resource.Test(t, resource.TestCase{
		PreCheck:     func() { testAccPreCheck(t) },
		Providers:    testAccProviders,
		CheckDestroy: nil,
		Steps: []resource.TestStep{
			{
				Config: testAccUserGroupCreate(rName, gid, posixName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("jumpcloud_user_group.test_group", "name", rName),
					resource.TestCheckResourceAttr("jumpcloud_user_group.test_group",
						"attributes.posix_groups", fmt.Sprintf("%d:%s", gid, posixName)),
					resource.TestCheckResourceAttr("jumpcloud_user_group.test_group", "members.#", "123"),
					resource.TestCheckResourceAttr("jumpcloud_user_group.test_group", "members.0", emails[0]),
					resource.TestCheckResourceAttr("jumpcloud_user_group.test_group", "members.60", emails[60]),
					resource.TestCheckResourceAttr("jumpcloud_user_group.test_group", "members.99", emails[99]),
					resource.TestCheckResourceAttr("jumpcloud_user_group.test_group", "members.100", emails[100]),
					resource.TestCheckResourceAttr("jumpcloud_user_group.test_group", "members.122", emails[122]),
				),
			},
			{
				Config: testAccUserGroupUpdate(rName, gid, posixName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("jumpcloud_user_group.test_group", "members.#", "2"),
					resource.TestCheckResourceAttr("jumpcloud_user_group.test_group", "members.0", fmt.Sprintf("%s1@testorg.com", rName)),
					resource.TestCheckResourceAttr("jumpcloud_user_group.test_group", "members.1", fmt.Sprintf("%s2@testorg.com", rName)),
				),
			},
			{
				Config: testAccUserGroupExternalAddRemove(rName, gid, posixName, t),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("jumpcloud_user_group.test_group", "members.#", "2"),
					resource.TestCheckResourceAttr("jumpcloud_user_group.test_group", "members.0", fmt.Sprintf("%s1@testorg.com", rName)),
					resource.TestCheckResourceAttr("jumpcloud_user_group.test_group", "members.1", fmt.Sprintf("%s2@testorg.com", rName)),
				),
			},
		},
	})
}

func testAccUserGroupCreate(name string, gid int, posixName string) string {
	return fmt.Sprintf(`
		resource "jumpcloud_user" "test_users" {
			count = 123 #test pagination on group membership

			username = "%[1]s${count.index}"
			email = "%[1]s${count.index}@testorg.com"
			firstname = "Firstname"
			lastname = "Lastname"
			enable_mfa = true
		}
		resource "jumpcloud_user_group" "test_group" {
    		name = "%[1]s"
			attributes = {
				posix_groups = "%[2]d:%[3]s"
			}
			members = jumpcloud_user.test_users[*].email
		}`, name, gid, posixName,
	)
}
func testAccUserGroupUpdate(name string, gid int, posixName string) string {
	return fmt.Sprintf(`
		resource "jumpcloud_user" "test_users" {
			count = 123 #test pagination on group membership

			username = "%[1]s${count.index}"
			email = "%[1]s${count.index}@testorg.com"
			firstname = "Firstname"
			lastname = "Lastname"
			enable_mfa = true
		}
		resource "jumpcloud_user_group" "test_group" {
    		name = "%[1]s"
			attributes = {
				posix_groups = "%[2]d:%[3]s"
			}
			members = [
				jumpcloud_user.test_users[2].email,
				jumpcloud_user.test_users[1].email,
			]
		}`, name, gid, posixName,
	)
}
func testAccUserGroupExternalAddRemove(name string, gid int, posixName string, t *testing.T) string {
	config := jcapiv2.NewConfiguration()
	config.AddDefaultHeader("x-api-key", os.Getenv("JUMPCLOUD_API_KEY"))
	client := jcapiv2.NewAPIClient(config)

	groups, _, err := client.UserGroupsApi.GroupsUserList(context.Background(), "application/json", "application/json", map[string]interface{}{
		"filter": fmt.Sprintf(`{"name":"%s"}`, name),
		"limit":  int32(1),
		"sort":   []string{},
	})
	if err != nil {
		t.Fatal(err)
	}

	email := make([]interface{}, 1)
	email[0] = fmt.Sprintf("%s43@testorg.com", name)

    time.Sleep(100 * time.Second)
	ids, err := userEmailsToIDs(config, email)
	if err != nil {
		t.Fatal(err)
	}
	payload := jcapiv2.UserGroupMembersReq{
		Op:    "add",
		Type_: "user",
		Id:    ids[0],
	}

	req := map[string]interface{}{
		"body": payload,
	}

    res, err := client.UserGroupMembersMembershipApi.GraphUserGroupMembersPost(
		context.TODO(), groups[0].Id, "", "", req)

	if err != nil {
		t.Fatal(fmt.Errorf("error adding user to group via api. error: %s, res: %+v", err, res))
	}

	return fmt.Sprintf(`
		resource "jumpcloud_user" "test_users" {
			count = 123 #test pagination on group membership

			username = "%[1]s${count.index}"
			email = "%[1]s${count.index}@testorg.com"
			firstname = "Firstname"
			lastname = "Lastname"
			enable_mfa = true
		}
		resource "jumpcloud_user_group" "test_group" {
    		name = "%[1]s"
			attributes = {
				posix_groups = "%[2]d:%[3]s"
			}
			members = [
				jumpcloud_user.test_users[2].email,
				jumpcloud_user.test_users[1].email,
			]
		}`, name, gid, posixName,
	)
}

func TestResourceUserGroup(t *testing.T) {
	suite.Run(t, new(ResourceUserGroupSuite))
}

type ResourceUserGroupSuite struct {
	suite.Suite
	A              *assert.Assertions
	TestHTTPServer *httptest.Server
}

func (s *ResourceUserGroupSuite) SetupSuite() {
	s.A = assert.New(s.Suite.T())
}

func (s *ResourceUserGroupSuite) TestTrueUserGroupRead() {
	cases := []struct {
		ResponseStatus int
		UserGroupNil   bool
		OK             bool
		ErrorNil       bool
		Payload        []byte
	}{
		{http.StatusNotFound, true, false, true, []byte("irrelevant")},
		{http.StatusOK, false, true, true, []byte("{}")},
	}

	for _, c := range cases {
		testServer := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			rw.WriteHeader(c.ResponseStatus)
			rw.Write(c.Payload)
		}))

		config := &jcapiv2.Configuration{
			BasePath: testServer.URL,
		}

		ug, ok, err := userGroupReadHelper(config, "id")
		s.A.Equal(c.OK, ok)
		s.A.Equal(c.UserGroupNil, ug == nil)
		s.A.Equal(c.ErrorNil, err == nil)
		testServer.Close()
	}
}
