package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"github.com/modfin/zdap/internal"
	"github.com/modfin/zdap/internal/config"
	"github.com/modfin/zdap/internal/core"
	"github.com/modfin/zdap/internal/servermodel"
	"github.com/modfin/zdap/internal/utils"
	"github.com/modfin/zdap/internal/zfs"
)

func Start(cfg *config.Config, app *core.Core, z *zfs.ZFS) error {
	e := echo.New()

	e.Use(middleware.RequestLogger())
	e.Use(middleware.RemoveTrailingSlash())
	e.Use(zfsStatsMiddleware)

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			auth := c.Request().Header.Get("auth")
			if len(auth) == 0 {
				fmt.Println(c.Request().Header)
				return errors.New("auth header must be supplied")
			}
			c.Set("owner", auth)
			return next(c)
		}
	})

	// /status reports counts and capacity, which the ZFS layer answers without
	// opening the pool, so this handler does not take a dataset.
	e.GET("/status", func(c *echo.Context) error {
		res, err := getStatus(c.Request().Context(), app)
		if err != nil {
			return fmt.Errorf("could not retrieve status, %w", err)
		}
		return c.JSON(http.StatusOK, res)
	})

	e.GET("/resources", func(c *echo.Context) error {
		lst, err := z.Load(c.Request().Context())
		if err != nil {
			return err
		}

		res, err := getResources(lst, c.Get("owner").(string), app)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, res)
	})

	e.GET("/resources/:resource", func(c *echo.Context) error {
		lst, err := z.Load(c.Request().Context())
		if err != nil {
			return err
		}

		res, err := getResource(lst, c.Get("owner").(string), c.Param("resource"), app)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, res)
	})

	e.GET("/resources/:resource/clones", func(c *echo.Context) error {
		lst, err := z.Load(c.Request().Context())
		if err != nil {
			return err
		}

		snaps, err := getSnaps(lst, c.Get("owner").(string), c.Param("resource"), app)
		if err != nil {
			return err
		}

		var clones []servermodel.ServerInternalClone

		for _, snap := range snaps {
			clones = append(clones, snap.Clones...)
		}

		return c.JSON(http.StatusOK, clones)
	})

	e.DELETE("/resources/:resource/clones", func(c *echo.Context) error {
		lst, err := z.Load(c.Request().Context())
		if err != nil {
			return err
		}

		snaps, err := getSnaps(lst, c.Get("owner").(string), c.Param("resource"), app)
		if err != nil {
			return err
		}
		for _, snap := range snaps {
			for _, clone := range snap.Clones {
				err = app.DestroyClone(lst, clone.Name)
				if err != nil {
					return err
				}
			}
		}
		return c.NoContent(http.StatusOK)
	})

	e.DELETE("/resources/:resource/clones/:time", func(c *echo.Context) error {
		lst, err := z.Load(c.Request().Context())
		if err != nil {
			return err
		}

		snaps, err := getSnaps(lst, c.Get("owner").(string), c.Param("resource"), app)
		if err != nil {
			return err
		}
		at, err := time.Parse(utils.TimestampFormat, c.Param("time"))
		if err != nil {
			return err
		}

		for _, snap := range snaps {
			for _, clone := range snap.Clones {
				if clone.CreatedAt.Equal(at) {
					err = app.DestroyClone(lst, clone.Name)
					if err != nil {
						return err
					}
					return c.NoContent(http.StatusOK)
				}
			}
		}
		return errors.New("could not find clone to destroy")
	})

	e.GET("/resources/:resource/snaps", func(c *echo.Context) error {
		lst, err := z.Load(c.Request().Context())
		if err != nil {
			return err
		}

		res, err := getSnaps(lst, c.Get("owner").(string), c.Param("resource"), app)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, res)
	})

	e.POST("/resources/:resource/snaps", func(c *echo.Context) error {
		resource := c.Param("resource")

		lst, err := z.Load(c.Request().Context())
		if err != nil {
			return err
		}

		snaps, err := getSnaps(lst, c.Get("owner").(string), resource, app)
		if err != nil {
			return err
		}
		var max time.Time
		for _, s := range snaps {
			if s.CreatedAt.After(max) {
				max = s.CreatedAt
			}
		}
		clone, err := app.CloneResource(lst, c.Get("owner").(string), resource, max)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, clone)
	})

	e.POST("/resources/:resource/snaps/:createdAt", func(c *echo.Context) error {
		resource := c.Param("resource")
		at, err := time.Parse(utils.TimestampFormat, c.Param("createdAt"))
		if err != nil {
			return err
		}

		lst, err := z.Load(c.Request().Context())
		if err != nil {
			return err
		}

		clone, err := app.CloneResource(lst, c.Get("owner").(string), resource, at)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, clone)
	})

	e.GET("/resources/:resource/snaps/:createdAt", func(c *echo.Context) error {
		at, err := time.Parse(utils.TimestampFormat, c.Param("createdAt"))
		if err != nil {
			return err
		}

		lst, err := z.Load(c.Request().Context())
		if err != nil {
			return err
		}

		res, err := getSnap(lst, c.Get("owner").(string), at, c.Param("resource"), app)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, res)
	})

	e.POST("/resources/:resource/claim", func(c *echo.Context) error {
		resource := c.Param("resource")
		timeoutStr := c.QueryParam("ttl")
		timeout := internal.DefaultClaimTimeoutSeconds * time.Second
		if timeoutStr != "" {
			t, err := strconv.ParseInt(timeoutStr, 10, 64)
			if err == nil {
				timeout = time.Duration(t) * time.Second
			}
		}

		clone, err := app.ClaimPooledClone(resource, timeout, c.Get("owner").(string))
		if err != nil {
			fmt.Println(err.Error())
			return c.JSON(http.StatusInternalServerError, err)
		}
		return c.JSON(http.StatusOK, clone)
	})

	e.DELETE("/resources/:resource/claims/:claimId", func(c *echo.Context) error {
		resource := c.Param("resource")
		claimId := c.Param("claimId")

		return app.ExpirePooledClone(resource, claimId)
	})

	registerZFSOpsRoutes(e)

	fmt.Println("== Loaded Resources ==")
	for _, r := range app.GetResourcesNames() {
		fmt.Println(" -", r)
	}
	fmt.Println("== Starting Cron ==")
	fmt.Println(app.Start())

	fmt.Println("== Starting API Server ==")
	return e.Start(fmt.Sprintf(":%d", cfg.APIPort))
}
