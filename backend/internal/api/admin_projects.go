package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/project"
)

// AdminProjectDTO is the full, management-facing shape of a Project. Unlike the
// public ProjectDTO it exposes the id, sort_order, and timestamps the admin
// console needs.
type AdminProjectDTO struct {
	ID          int      `json:"id"`
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	Summary     string   `json:"summary"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	RepoURL     string   `json:"repo_url"`
	LiveURL     string   `json:"live_url"`
	ImageKeys   []string `json:"image_keys"`
	Featured    bool     `json:"featured"`
	SortOrder   int      `json:"sort_order"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

func toAdminProjectDTO(p *ent.Project) AdminProjectDTO {
	return AdminProjectDTO{
		ID:          p.ID,
		Slug:        p.Slug,
		Title:       p.Title,
		Summary:     p.Summary,
		Description: p.Description,
		Tags:        p.Tags,
		RepoURL:     p.RepoURL,
		LiveURL:     p.LiveURL,
		ImageKeys:   p.ImageKeys,
		Featured:    p.Featured,
		SortOrder:   p.SortOrder,
		CreatedAt:   p.CreatedAt.UTC().Format(http.TimeFormat),
		UpdatedAt:   p.UpdatedAt.UTC().Format(http.TimeFormat),
	}
}

type listAdminProjectsOutput struct {
	Body struct {
		Projects []AdminProjectDTO `json:"projects"`
	}
}

type adminProjectOutput struct {
	Body AdminProjectDTO
}

type createProjectInput struct {
	Body struct {
		Slug        string   `json:"slug" minLength:"1" maxLength:"120" doc:"URL-friendly unique identifier"`
		Title       string   `json:"title" minLength:"1"`
		Summary     string   `json:"summary" maxLength:"280" doc:"Short one-liner shown in cards/lists"`
		Description string   `json:"description,omitempty" doc:"Longer markdown body"`
		Tags        []string `json:"tags,omitempty"`
		RepoURL     string   `json:"repo_url,omitempty"`
		LiveURL     string   `json:"live_url,omitempty"`
		ImageKeys   []string `json:"image_keys,omitempty" doc:"S3 object keys returned by the upload presign endpoint"`
		Featured    bool     `json:"featured,omitempty"`
		SortOrder   int      `json:"sort_order,omitempty" doc:"Lower sorts first"`
	}
}

// updateProjectInput is a PATCH: every field is a pointer so an omitted field
// leaves the column untouched. Both the featured toggle and sort_order are
// updatable here.
type updateProjectInput struct {
	ID   int `path:"id" doc:"Project ID"`
	Body struct {
		Slug        *string   `json:"slug,omitempty" minLength:"1" maxLength:"120"`
		Title       *string   `json:"title,omitempty" minLength:"1"`
		Summary     *string   `json:"summary,omitempty" maxLength:"280"`
		Description *string   `json:"description,omitempty"`
		Tags        *[]string `json:"tags,omitempty"`
		RepoURL     *string   `json:"repo_url,omitempty"`
		LiveURL     *string   `json:"live_url,omitempty"`
		ImageKeys   *[]string `json:"image_keys,omitempty"`
		Featured    *bool     `json:"featured,omitempty"`
		SortOrder   *int      `json:"sort_order,omitempty"`
	}
}

type projectIDInput struct {
	ID int `path:"id" doc:"Project ID"`
}

type reorderProjectsInput struct {
	Body struct {
		Items []struct {
			ID        int `json:"id"`
			SortOrder int `json:"sort_order"`
		} `json:"items" doc:"New sort_order for each project id"`
	}
}

func (h *Handler) registerAdminProjects(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-admin-projects",
		Method:      http.MethodGet,
		Path:        "/api/admin/projects",
		Summary:     "List all projects (including unfeatured) for management",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, _ *struct{}) (*listAdminProjectsOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		rows, err := h.deps.Ent.Project.Query().Order(ent.Asc(project.FieldSortOrder)).All(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to list projects", err)
		}
		out := &listAdminProjectsOutput{}
		out.Body.Projects = make([]AdminProjectDTO, 0, len(rows))
		for _, p := range rows {
			out.Body.Projects = append(out.Body.Projects, toAdminProjectDTO(p))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "create-project",
		Method:        http.MethodPost,
		Path:          "/api/admin/projects",
		Summary:       "Create a project",
		Tags:          adminTags,
		Security:      cookieAuthSecurity,
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *createProjectInput) (*adminProjectOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		p, err := h.deps.Ent.Project.Create().
			SetSlug(in.Body.Slug).
			SetTitle(in.Body.Title).
			SetSummary(in.Body.Summary).
			SetDescription(in.Body.Description).
			SetTags(in.Body.Tags).
			SetRepoURL(in.Body.RepoURL).
			SetLiveURL(in.Body.LiveURL).
			SetImageKeys(in.Body.ImageKeys).
			SetFeatured(in.Body.Featured).
			SetSortOrder(in.Body.SortOrder).
			Save(ctx)
		if ent.IsConstraintError(err) {
			return nil, huma.Error409Conflict("a project with that slug already exists")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to create project", err)
		}
		return &adminProjectOutput{Body: toAdminProjectDTO(p)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "update-project",
		Method:      http.MethodPatch,
		Path:        "/api/admin/projects/{id}",
		Summary:     "Update a project (partial; includes featured toggle and sort_order)",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, in *updateProjectInput) (*adminProjectOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		upd := h.deps.Ent.Project.UpdateOneID(in.ID)
		if in.Body.Slug != nil {
			upd.SetSlug(*in.Body.Slug)
		}
		if in.Body.Title != nil {
			upd.SetTitle(*in.Body.Title)
		}
		if in.Body.Summary != nil {
			upd.SetSummary(*in.Body.Summary)
		}
		if in.Body.Description != nil {
			upd.SetDescription(*in.Body.Description)
		}
		if in.Body.Tags != nil {
			upd.SetTags(*in.Body.Tags)
		}
		if in.Body.RepoURL != nil {
			upd.SetRepoURL(*in.Body.RepoURL)
		}
		if in.Body.LiveURL != nil {
			upd.SetLiveURL(*in.Body.LiveURL)
		}
		if in.Body.ImageKeys != nil {
			upd.SetImageKeys(*in.Body.ImageKeys)
		}
		if in.Body.Featured != nil {
			upd.SetFeatured(*in.Body.Featured)
		}
		if in.Body.SortOrder != nil {
			upd.SetSortOrder(*in.Body.SortOrder)
		}
		p, err := upd.Save(ctx)
		if ent.IsNotFound(err) {
			return nil, huma.Error404NotFound("project not found")
		}
		if ent.IsConstraintError(err) {
			return nil, huma.Error409Conflict("a project with that slug already exists")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to update project", err)
		}
		return &adminProjectOutput{Body: toAdminProjectDTO(p)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "delete-project",
		Method:        http.MethodDelete,
		Path:          "/api/admin/projects/{id}",
		Summary:       "Delete a project",
		Tags:          adminTags,
		Security:      cookieAuthSecurity,
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *projectIDInput) (*struct{}, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		err := h.deps.Ent.Project.DeleteOneID(in.ID).Exec(ctx)
		if ent.IsNotFound(err) {
			return nil, huma.Error404NotFound("project not found")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to delete project", err)
		}
		return &struct{}{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "reorder-projects",
		Method:      http.MethodPost,
		Path:        "/api/admin/projects/reorder",
		Summary:     "Set sort_order for a set of projects in one call",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, in *reorderProjectsInput) (*listAdminProjectsOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		tx, err := h.deps.Ent.Tx(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to reorder projects", err)
		}
		for _, it := range in.Body.Items {
			if err := tx.Project.UpdateOneID(it.ID).SetSortOrder(it.SortOrder).Exec(ctx); err != nil {
				_ = tx.Rollback()
				if ent.IsNotFound(err) {
					return nil, huma.Error404NotFound("project not found")
				}
				return nil, huma.Error500InternalServerError("failed to reorder projects", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return nil, huma.Error500InternalServerError("failed to reorder projects", err)
		}
		rows, err := h.deps.Ent.Project.Query().Order(ent.Asc(project.FieldSortOrder)).All(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to load projects", err)
		}
		out := &listAdminProjectsOutput{}
		out.Body.Projects = make([]AdminProjectDTO, 0, len(rows))
		for _, p := range rows {
			out.Body.Projects = append(out.Body.Projects, toAdminProjectDTO(p))
		}
		return out, nil
	})
}
