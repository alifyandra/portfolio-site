package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/project"
)

// ProjectDTO is the frontend-facing shape of a Project.
type ProjectDTO struct {
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	Summary     string   `json:"summary"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	RepoURL     string   `json:"repo_url,omitempty"`
	LiveURL     string   `json:"live_url,omitempty"`
	ImageKeys   []string `json:"image_keys"`
	Featured    bool     `json:"featured"`
}

func toProjectDTO(p *ent.Project) ProjectDTO {
	return ProjectDTO{
		Slug:        p.Slug,
		Title:       p.Title,
		Summary:     p.Summary,
		Description: p.Description,
		Tags:        p.Tags,
		RepoURL:     p.RepoURL,
		LiveURL:     p.LiveURL,
		ImageKeys:   p.ImageKeys,
		Featured:    p.Featured,
	}
}

// --- List ---

type listProjectsInput struct {
	Featured bool `query:"featured" doc:"If true, only featured projects"`
}

type listProjectsOutput struct {
	Body struct {
		Projects []ProjectDTO `json:"projects"`
	}
}

// --- Get ---

type getProjectInput struct {
	Slug string `path:"slug" doc:"Project slug"`
}

type getProjectOutput struct {
	Body ProjectDTO
}

func (h *Handler) registerProjects(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-projects",
		Method:      http.MethodGet,
		Path:        "/api/projects",
		Summary:     "List projects",
		Tags:        []string{"projects"},
	}, func(ctx context.Context, in *listProjectsInput) (*listProjectsOutput, error) {
		q := h.deps.Ent.Project.Query()
		if in.Featured {
			q = q.Where(project.FeaturedEQ(true))
		}
		rows, err := q.Order(ent.Asc(project.FieldSortOrder)).All(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to list projects", err)
		}
		out := &listProjectsOutput{}
		out.Body.Projects = make([]ProjectDTO, 0, len(rows))
		for _, p := range rows {
			out.Body.Projects = append(out.Body.Projects, toProjectDTO(p))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-project",
		Method:      http.MethodGet,
		Path:        "/api/projects/{slug}",
		Summary:     "Get a project by slug",
		Tags:        []string{"projects"},
	}, func(ctx context.Context, in *getProjectInput) (*getProjectOutput, error) {
		p, err := h.deps.Ent.Project.Query().Where(project.SlugEQ(in.Slug)).Only(ctx)
		if ent.IsNotFound(err) {
			return nil, huma.Error404NotFound("project not found")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to get project", err)
		}
		return &getProjectOutput{Body: toProjectDTO(p)}, nil
	})

	// Projects are read-only over the API. Creation happens via the seed command
	// (`make seed`) until an authenticated admin exists — no public write path.
}
