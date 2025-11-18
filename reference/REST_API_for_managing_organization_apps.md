# REST API for managing organization GitHub App installations - GitHub Enterprise Cloud Docs
Use the REST API to manage which GitHub Apps are installed in your enterprise's organizations.

Get enterprise-owned organizations that can have GitHub Apps installed
-------------------------------------------------------------------------------------------------------------------------------------------------

List the organizations owned by the enterprise, intended for use by GitHub Apps that are managing applications across the enterprise.

This API can only be called by a GitHub App installed on the enterprise that owns the organization.

### Fine-grained access tokens for "Get enterprise-owned organizations that can have GitHub Apps installed"

This endpoint works with the following fine-grained token types:

*   GitHub App user access tokens
*   GitHub App installation access tokens

The fine-grained token must have the following permission set:

*   "Enterprise organization installations" enterprise permissions (read)

### Parameters for "Get enterprise-owned organizations that can have GitHub Apps installed"


Headers

|Name, Type, Description                                             |
|--------------------------------------------------------------------|
|accept string Setting to application/vnd.github+json is recommended.|



Path parameters

|Name, Type, Description                                           |
|------------------------------------------------------------------|
|enterprise string RequiredThe slug version of the enterprise name.|



Query parameters


* Name, Type, Description: per_page integer The number of results per page (max 100). For more information, see "Using pagination in the REST API."Default: 30
* Name, Type, Description: page integer The page number of the results to fetch. For more information, see "Using pagination in the REST API."Default: 1


### HTTP response status codes for "Get enterprise-owned organizations that can have GitHub Apps installed"



* Status code: 200
  * Description: A list of organizations owned by the enterprise on which the authenticated GitHub App is installed.


### Code samples for "Get enterprise-owned organizations that can have GitHub Apps installed"

If you access GitHub at GHE.com, replace `api.github.com` with your enterprise's dedicated subdomain at `api.SUBDOMAIN.ghe.com`.

#### Request example

get/enterprises/{enterprise}/apps/installable\_organizations

`curl -L \ -H "Accept: application/vnd.github+json" \ -H "Authorization: Bearer <YOUR-TOKEN>" \ -H "X-GitHub-Api-Version: 2022-11-28" \ https://api.github.com/enterprises/ENTERPRISE/apps/installable_organizations`

#### 

A list of organizations owned by the enterprise on which the authenticated GitHub App is installed.

`Status: 200`

`[ { "id": 1, "login": "github" }, { "id": 2, "login": "microsoft" } ]`

Get repositories belonging to an enterprise-owned organization
---------------------------------------------------------------------------------------------------------------------------------

List the repositories belonging to an enterprise-owned organization that can be made accessible to a GitHub App installed on that organization. This API provides a shallow list of repositories in the organization, allowing the caller to then add or remove those repositories to an installation in that organization.

This API can only be called by a GitHub App installed on the enterprise that owns the organization.

### Fine-grained access tokens for "Get repositories belonging to an enterprise-owned organization"

This endpoint works with the following fine-grained token types:

*   GitHub App user access tokens
*   GitHub App installation access tokens

The fine-grained token must have at least one of the following permission sets:

*   "Enterprise organization installation repositories" enterprise permissions (read)
*   "Enterprise organization installations" enterprise permissions (read)

### Parameters for "Get repositories belonging to an enterprise-owned organization"


Headers

|Name, Type, Description                                             |
|--------------------------------------------------------------------|
|accept string Setting to application/vnd.github+json is recommended.|



Path parameters

|Name, Type, Description                                                  |
|-------------------------------------------------------------------------|
|enterprise string RequiredThe slug version of the enterprise name.       |
|org string RequiredThe organization name. The name is not case sensitive.|



Query parameters


* Name, Type, Description: per_page integer The number of results per page (max 100). For more information, see "Using pagination in the REST API."Default: 30
* Name, Type, Description: page integer The page number of the results to fetch. For more information, see "Using pagination in the REST API."Default: 1


### HTTP response status codes for "Get repositories belonging to an enterprise-owned organization"



* Status code: 200
  * Description: A list of repositories owned by the enterprise organization on which the authenticated GitHub App is installed.


### Code samples for "Get repositories belonging to an enterprise-owned organization"

If you access GitHub at GHE.com, replace `api.github.com` with your enterprise's dedicated subdomain at `api.SUBDOMAIN.ghe.com`.

#### Request example

get/enterprises/{enterprise}/apps/installable\_organizations/{org}/accessible\_repositories

`curl -L \ -H "Accept: application/vnd.github+json" \ -H "Authorization: Bearer <YOUR-TOKEN>" \ -H "X-GitHub-Api-Version: 2022-11-28" \ https://api.github.com/enterprises/ENTERPRISE/apps/installable_organizations/ORG/accessible_repositories`

#### 

A list of repositories owned by the enterprise organization on which the authenticated GitHub App is installed.

`Status: 200`

`[ { "id": 1, "name": "Hello World", "full_name": "octocat/Hello-World" }, { "id": 2, "login": "Goodbye World", "full_name": "octocat/Goodbye-World" } ]`

List GitHub Apps installed on an enterprise-owned organization
---------------------------------------------------------------------------------------------------------------------------------

Lists the GitHub App installations associated with the given enterprise-owned organization. This lists all GitHub Apps that have been installed on the organization, regardless of who owns the application.

This API can only be called by a GitHub App installed on the enterprise that owns the organization.

### Fine-grained access tokens for "List GitHub Apps installed on an enterprise-owned organization"

This endpoint works with the following fine-grained token types:

*   GitHub App user access tokens
*   GitHub App installation access tokens

The fine-grained token must have the following permission set:

*   "Enterprise organization installations" enterprise permissions (read)

### Parameters for "List GitHub Apps installed on an enterprise-owned organization"


Headers

|Name, Type, Description                                             |
|--------------------------------------------------------------------|
|accept string Setting to application/vnd.github+json is recommended.|



Path parameters

|Name, Type, Description                                                  |
|-------------------------------------------------------------------------|
|enterprise string RequiredThe slug version of the enterprise name.       |
|org string RequiredThe organization name. The name is not case sensitive.|



Query parameters


* Name, Type, Description: per_page integer The number of results per page (max 100). For more information, see "Using pagination in the REST API."Default: 30
* Name, Type, Description: page integer The page number of the results to fetch. For more information, see "Using pagination in the REST API."Default: 1


### HTTP response status codes for "List GitHub Apps installed on an enterprise-owned organization"


|Status code|Description                                                                         |
|-----------|------------------------------------------------------------------------------------|
|200        |A list of GitHub App installations that have been granted access to the organization|


### Code samples for "List GitHub Apps installed on an enterprise-owned organization"

If you access GitHub at GHE.com, replace `api.github.com` with your enterprise's dedicated subdomain at `api.SUBDOMAIN.ghe.com`.

#### Request example

get/enterprises/{enterprise}/apps/organizations/{org}/installations

`curl -L \ -H "Accept: application/vnd.github+json" \ -H "Authorization: Bearer <YOUR-TOKEN>" \ -H "X-GitHub-Api-Version: 2022-11-28" \ https://api.github.com/enterprises/ENTERPRISE/apps/organizations/ORG/installations`

#### 

A list of GitHub App installations that have been granted access to the organization

`Status: 200`

`[ { "value": { "id": 1, "app_slug": "monalisa/orbit", "repository_selection": "selected", "repositories_url": "https://api.github.com/enterprises/acme-corp/apps/organizations/some-org/installations/1/repositories", "permissions": { "checks": "write", "metadata": "read", "contents": "read" }, "events": [ "push", "pull_request" ], "created_at": "2017-07-08T16:18:44-04:00", "updated_at": "2017-07-08T16:18:44-04:00" } } ]`

Install a GitHub App on an enterprise-owned organization
---------------------------------------------------------------------------------------------------------------------

Installs any valid GitHub App on the specified organization owned by the enterprise. If the app is already installed on the organization, and is suspended, it will be unsuspended. If the app has a pending installation request, they will all be approved.

If the app is already installed and has a pending update request, it will be updated to the latest version. If the app is now requesting repository permissions, it will be given access to the repositories listed in the request or fail if no `repository_selection` is provided.

This API can only be called by a GitHub App installed on the enterprise that owns the organization.

### Fine-grained access tokens for "Install a GitHub App on an enterprise-owned organization"

This endpoint works with the following fine-grained token types:

*   GitHub App user access tokens
*   GitHub App installation access tokens

The fine-grained token must have the following permission set:

*   "Enterprise organization installations" enterprise permissions (write)

### Parameters for "Install a GitHub App on an enterprise-owned organization"


Headers

|Name, Type, Description                                             |
|--------------------------------------------------------------------|
|accept string Setting to application/vnd.github+json is recommended.|



Path parameters

|Name, Type, Description                                                  |
|-------------------------------------------------------------------------|
|enterprise string RequiredThe slug version of the enterprise name.       |
|org string RequiredThe organization name. The name is not case sensitive.|



Body parameters


* Name, Type, Description: client_id string RequiredThe Client ID of the GitHub App to install.
* Name, Type, Description: repository_selection string RequiredThe repository selection for the GitHub App. Must be one of:all - the installation can access all repositories in the organization.selected - the installation can access only the listed repositories.none - no repository permissions are requested. Only use when the app does not request repository permissions.Can be one of: all, selected, none 
* Name, Type, Description: repositories array of strings The names of the repositories to which the installation will be granted access. This is the simple name of the repository, not the full name (e.g., hello-world not octocat/hello-world). This is only required when repository_selection is selected.


### HTTP response status codes for "Install a GitHub App on an enterprise-owned organization"


|Status code|Description                                             |
|-----------|--------------------------------------------------------|
|200        |A GitHub App installation that was installed previously.|
|201        |A GitHub App installation.                              |


### Code samples for "Install a GitHub App on an enterprise-owned organization"

If you access GitHub at GHE.com, replace `api.github.com` with your enterprise's dedicated subdomain at `api.SUBDOMAIN.ghe.com`.

#### Request example

post/enterprises/{enterprise}/apps/organizations/{org}/installations

`curl -L \ -X POST \ -H "Accept: application/vnd.github+json" \ -H "Authorization: Bearer <YOUR-TOKEN>" \ -H "X-GitHub-Api-Version: 2022-11-28" \ https://api.github.com/enterprises/ENTERPRISE/apps/organizations/ORG/installations \ -d '{"client_id":"Iv2abc123aabbcc","repository_selection":"all"}'`

#### 

A GitHub App installation that was installed previously.

`Status: 200`

`{ "id": 1, "account": { "login": "octocat", "id": 1, "node_id": "MDQ6VXNlcjE=", "avatar_url": "https://github.com/images/error/octocat_happy.gif", "gravatar_id": "", "url": "https://api.github.com/users/octocat", "html_url": "https://github.com/octocat", "followers_url": "https://api.github.com/users/octocat/followers", "following_url": "https://api.github.com/users/octocat/following{/other_user}", "gists_url": "https://api.github.com/users/octocat/gists{/gist_id}", "starred_url": "https://api.github.com/users/octocat/starred{/owner}{/repo}", "subscriptions_url": "https://api.github.com/users/octocat/subscriptions", "organizations_url": "https://api.github.com/users/octocat/orgs", "repos_url": "https://api.github.com/users/octocat/repos", "events_url": "https://api.github.com/users/octocat/events{/privacy}", "received_events_url": "https://api.github.com/users/octocat/received_events", "type": "User", "site_admin": false }, "access_tokens_url": "https://api.github.com/app/installations/1/access_tokens", "repositories_url": "https://api.github.com/installation/repositories", "html_url": "https://github.com/organizations/github/settings/installations/1", "app_id": 1, "target_id": 1, "target_type": "Organization", "permissions": { "checks": "write", "metadata": "read", "contents": "read" }, "events": [ "push", "pull_request" ], "single_file_name": "config.yaml", "has_multiple_single_files": true, "single_file_paths": [ "config.yml", ".github/issue_TEMPLATE.md" ], "repository_selection": "selected", "created_at": "2017-07-08T16:18:44-04:00", "updated_at": "2017-07-08T16:18:44-04:00", "app_slug": "github-actions", "suspended_at": null, "suspended_by": null }`

Uninstall a GitHub App from an enterprise-owned organization
-----------------------------------------------------------------------------------------------------------------------------

Uninstall a GitHub App from an organization. Any app installed on the organization can be removed.

This API can only be called by a GitHub App installed on the enterprise that owns the organization.

### Fine-grained access tokens for "Uninstall a GitHub App from an enterprise-owned organization"

This endpoint works with the following fine-grained token types:

*   GitHub App user access tokens
*   GitHub App installation access tokens

The fine-grained token must have the following permission set:

*   "Enterprise organization installations" enterprise permissions (write)

### Parameters for "Uninstall a GitHub App from an enterprise-owned organization"


Headers

|Name, Type, Description                                             |
|--------------------------------------------------------------------|
|accept string Setting to application/vnd.github+json is recommended.|



Path parameters

|Name, Type, Description                                                   |
|--------------------------------------------------------------------------|
|enterprise string RequiredThe slug version of the enterprise name.        |
|org string RequiredThe organization name. The name is not case sensitive. |
|installation_id integer RequiredThe unique identifier of the installation.|


### HTTP response status codes for "Uninstall a GitHub App from an enterprise-owned organization"


|Status code|Description                                                                |
|-----------|---------------------------------------------------------------------------|
|204        |An empty response indicates that the installation was successfully removed.|
|403        |Forbidden                                                                  |
|404        |Resource not found                                                         |


### Code samples for "Uninstall a GitHub App from an enterprise-owned organization"

If you access GitHub at GHE.com, replace `api.github.com` with your enterprise's dedicated subdomain at `api.SUBDOMAIN.ghe.com`.

#### Request example

delete/enterprises/{enterprise}/apps/organizations/{org}/installations/{installation\_id}

`curl -L \ -X DELETE \ -H "Accept: application/vnd.github+json" \ -H "Authorization: Bearer <YOUR-TOKEN>" \ -H "X-GitHub-Api-Version: 2022-11-28" \ https://api.github.com/enterprises/ENTERPRISE/apps/organizations/ORG/installations/1`

#### 

An empty response indicates that the installation was successfully removed.

Get the repositories accessible to a given GitHub App installation
-----------------------------------------------------------------------------------------------------------------------------------------

Lists the repositories accessible to a given GitHub App installation on an enterprise-owned organization.

This API can only be called by a GitHub App installed on the enterprise that owns the organization.

### Fine-grained access tokens for "Get the repositories accessible to a given GitHub App installation"

This endpoint works with the following fine-grained token types:

*   GitHub App user access tokens
*   GitHub App installation access tokens

The fine-grained token must have at least one of the following permission sets:

*   "Enterprise organization installation repositories" enterprise permissions (read)
*   "Enterprise organization installations" enterprise permissions (read)

### Parameters for "Get the repositories accessible to a given GitHub App installation"


Headers

|Name, Type, Description                                             |
|--------------------------------------------------------------------|
|accept string Setting to application/vnd.github+json is recommended.|



Path parameters

|Name, Type, Description                                                   |
|--------------------------------------------------------------------------|
|enterprise string RequiredThe slug version of the enterprise name.        |
|org string RequiredThe organization name. The name is not case sensitive. |
|installation_id integer RequiredThe unique identifier of the installation.|



Query parameters


* Name, Type, Description: per_page integer The number of results per page (max 100). For more information, see "Using pagination in the REST API."Default: 30
* Name, Type, Description: page integer The page number of the results to fetch. For more information, see "Using pagination in the REST API."Default: 1


### HTTP response status codes for "Get the repositories accessible to a given GitHub App installation"



* Status code: 200
  * Description: A list of repositories owned by the enterprise organization to which the installation has access.


### Code samples for "Get the repositories accessible to a given GitHub App installation"

If you access GitHub at GHE.com, replace `api.github.com` with your enterprise's dedicated subdomain at `api.SUBDOMAIN.ghe.com`.

#### Request example

get/enterprises/{enterprise}/apps/organizations/{org}/installations/{installation\_id}/repositories

`curl -L \ -H "Accept: application/vnd.github+json" \ -H "Authorization: Bearer <YOUR-TOKEN>" \ -H "X-GitHub-Api-Version: 2022-11-28" \ https://api.github.com/enterprises/ENTERPRISE/apps/organizations/ORG/installations/1/repositories`

#### 

A list of repositories owned by the enterprise organization to which the installation has access.

`Status: 200`

`[ { "id": 1, "name": "Hello World", "full_name": "octocat/Hello-World" }, { "id": 2, "login": "Goodbye World", "full_name": "octocat/Goodbye-World" } ]`

Toggle installation repository access between selected and all repositories
-----------------------------------------------------------------------------------------------------------------------------------------------------------

Toggle repository access for a GitHub App installation between all repositories and selected repositories. You must provide at least one repository when changing the access to 'selected'. If you change the access to 'all', the repositories list must not be provided.

This API can only be called by a GitHub App installed on the enterprise that owns the organization.

### Fine-grained access tokens for "Toggle installation repository access between selected and all repositories"

This endpoint works with the following fine-grained token types:

*   GitHub App user access tokens
*   GitHub App installation access tokens

The fine-grained token must have at least one of the following permission sets:

*   "Enterprise organization installation repositories" enterprise permissions (write)
*   "Enterprise organization installations" enterprise permissions (write)

### Parameters for "Toggle installation repository access between selected and all repositories"


Headers

|Name, Type, Description                                             |
|--------------------------------------------------------------------|
|accept string Setting to application/vnd.github+json is recommended.|



Path parameters

|Name, Type, Description                                                   |
|--------------------------------------------------------------------------|
|enterprise string RequiredThe slug version of the enterprise name.        |
|org string RequiredThe organization name. The name is not case sensitive. |
|installation_id integer RequiredThe unique identifier of the installation.|



Body parameters


* Name, Type, Description: repository_selection string RequiredOne of either 'all' or 'selected'Can be one of: all, selected 
* Name, Type, Description: repositories array of strings The repository names to add to the installation. Only required when repository_selection is 'selected'


### HTTP response status codes for "Toggle installation repository access between selected and all repositories"


|Status code|Description                                  |
|-----------|---------------------------------------------|
|200        |The GitHub App installation that was updated.|


### Code samples for "Toggle installation repository access between selected and all repositories"

If you access GitHub at GHE.com, replace `api.github.com` with your enterprise's dedicated subdomain at `api.SUBDOMAIN.ghe.com`.

#### Request example

patch/enterprises/{enterprise}/apps/organizations/{org}/installations/{installation\_id}/repositories

`curl -L \ -X PATCH \ -H "Accept: application/vnd.github+json" \ -H "Authorization: Bearer <YOUR-TOKEN>" \ -H "X-GitHub-Api-Version: 2022-11-28" \ https://api.github.com/enterprises/ENTERPRISE/apps/organizations/ORG/installations/1/repositories \ -d '{"repository_selection":"selected","repositories":["hello-world","hello-world-2"]}'`

#### 

The GitHub App installation that was updated.

`Status: 200`

`{ "id": 1, "account": { "login": "octocat", "id": 1, "node_id": "MDQ6VXNlcjE=", "avatar_url": "https://github.com/images/error/octocat_happy.gif", "gravatar_id": "", "url": "https://api.github.com/users/octocat", "html_url": "https://github.com/octocat", "followers_url": "https://api.github.com/users/octocat/followers", "following_url": "https://api.github.com/users/octocat/following{/other_user}", "gists_url": "https://api.github.com/users/octocat/gists{/gist_id}", "starred_url": "https://api.github.com/users/octocat/starred{/owner}{/repo}", "subscriptions_url": "https://api.github.com/users/octocat/subscriptions", "organizations_url": "https://api.github.com/users/octocat/orgs", "repos_url": "https://api.github.com/users/octocat/repos", "events_url": "https://api.github.com/users/octocat/events{/privacy}", "received_events_url": "https://api.github.com/users/octocat/received_events", "type": "User", "site_admin": false }, "access_tokens_url": "https://api.github.com/app/installations/1/access_tokens", "repositories_url": "https://api.github.com/installation/repositories", "html_url": "https://github.com/organizations/github/settings/installations/1", "app_id": 1, "target_id": 1, "target_type": "Organization", "permissions": { "checks": "write", "metadata": "read", "contents": "read" }, "events": [ "push", "pull_request" ], "single_file_name": "config.yaml", "has_multiple_single_files": true, "single_file_paths": [ "config.yml", ".github/issue_TEMPLATE.md" ], "repository_selection": "selected", "created_at": "2017-07-08T16:18:44-04:00", "updated_at": "2017-07-08T16:18:44-04:00", "app_slug": "github-actions", "suspended_at": null, "suspended_by": null }`

Grant repository access to an organization installation
-------------------------------------------------------------------------------------------------------------------

Grant repository access to an organization installation. You can add up to 50 repositories at a time. If the installation already has access to the repository, it will not be added again.

This API can only be called by a GitHub App installed on the enterprise that owns the organization.

### Fine-grained access tokens for "Grant repository access to an organization installation"

This endpoint works with the following fine-grained token types:

*   GitHub App user access tokens
*   GitHub App installation access tokens

The fine-grained token must have at least one of the following permission sets:

*   "Enterprise organization installation repositories" enterprise permissions (write)
*   "Enterprise organization installations" enterprise permissions (write)

### Parameters for "Grant repository access to an organization installation"


Headers

|Name, Type, Description                                             |
|--------------------------------------------------------------------|
|accept string Setting to application/vnd.github+json is recommended.|



Path parameters

|Name, Type, Description                                                   |
|--------------------------------------------------------------------------|
|enterprise string RequiredThe slug version of the enterprise name.        |
|org string RequiredThe organization name. The name is not case sensitive. |
|installation_id integer RequiredThe unique identifier of the installation.|



Body parameters

|Name, Type, Description                                                               |
|--------------------------------------------------------------------------------------|
|repositories array of strings RequiredThe repository names to add to the installation.|


### HTTP response status codes for "Grant repository access to an organization installation"



* Status code: 200
  * Description: A list of repositories which the authenticated GitHub App should be granted access to.


### Code samples for "Grant repository access to an organization installation"

If you access GitHub at GHE.com, replace `api.github.com` with your enterprise's dedicated subdomain at `api.SUBDOMAIN.ghe.com`.

#### Request example

patch/enterprises/{enterprise}/apps/organizations/{org}/installations/{installation\_id}/repositories/add

`curl -L \ -X PATCH \ -H "Accept: application/vnd.github+json" \ -H "Authorization: Bearer <YOUR-TOKEN>" \ -H "X-GitHub-Api-Version: 2022-11-28" \ https://api.github.com/enterprises/ENTERPRISE/apps/organizations/ORG/installations/1/repositories/add \ -d '{"repositories":["hello-world","hello-world-2"]}'`

#### 

A list of repositories which the authenticated GitHub App should be granted access to.

`Status: 200`

`[ { "id": 1, "name": "Hello World", "full_name": "octocat/Hello-World" }, { "id": 2, "login": "Goodbye World", "full_name": "octocat/Goodbye-World" } ]`

Remove repository access from an organization installation
-------------------------------------------------------------------------------------------------------------------------

Remove repository access from a GitHub App installed on an organization. You can remove up to 50 repositories at a time. You cannot remove repositories from an app installed on `all` repositories, nor can you remove the last repository for an app. If you attempt to do so, the API will return a 422 Unprocessable Entity error.

This API can only be called by a GitHub App installed on the enterprise that owns the organization.

### Fine-grained access tokens for "Remove repository access from an organization installation"

This endpoint works with the following fine-grained token types:

*   GitHub App user access tokens
*   GitHub App installation access tokens

The fine-grained token must have at least one of the following permission sets:

*   "Enterprise organization installation repositories" enterprise permissions (write)
*   "Enterprise organization installations" enterprise permissions (write)

### Parameters for "Remove repository access from an organization installation"


Headers

|Name, Type, Description                                             |
|--------------------------------------------------------------------|
|accept string Setting to application/vnd.github+json is recommended.|



Path parameters

|Name, Type, Description                                                   |
|--------------------------------------------------------------------------|
|enterprise string RequiredThe slug version of the enterprise name.        |
|org string RequiredThe organization name. The name is not case sensitive. |
|installation_id integer RequiredThe unique identifier of the installation.|



Body parameters

|Name, Type, Description                                                                    |
|-------------------------------------------------------------------------------------------|
|repositories array of strings RequiredThe repository names to remove from the installation.|


### HTTP response status codes for "Remove repository access from an organization installation"



* Status code: 200
  * Description: A list of repositories which the authenticated GitHub App has lost access to.
* Status code: 422
  * Description: The request was well-formed but was unable to be followed due to semantic errors. This can happen if you attempt to remove a repository from an installation that has access to all repositories, or if you attempt to remove the last repository from an installation.


### Code samples for "Remove repository access from an organization installation"

If you access GitHub at GHE.com, replace `api.github.com` with your enterprise's dedicated subdomain at `api.SUBDOMAIN.ghe.com`.

#### Request example

patch/enterprises/{enterprise}/apps/organizations/{org}/installations/{installation\_id}/repositories/remove

`curl -L \ -X PATCH \ -H "Accept: application/vnd.github+json" \ -H "Authorization: Bearer <YOUR-TOKEN>" \ -H "X-GitHub-Api-Version: 2022-11-28" \ https://api.github.com/enterprises/ENTERPRISE/apps/organizations/ORG/installations/1/repositories/remove \ -d '{"repositories":["hello-world","hello-world-2"]}'`

#### 

A list of repositories which the authenticated GitHub App has lost access to.

`Status: 200`

`[ { "id": 1, "name": "Hello World", "full_name": "octocat/Hello-World" }, { "id": 2, "login": "Goodbye World", "full_name": "octocat/Goodbye-World" } ]`