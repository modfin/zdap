package api

import (
	"errors"
	"fmt"
	"github.com/labstack/echo/v4/middleware"
	"github.com/modfin/zdap"
	"github.com/modfin/zdap/internal"
	"github.com/modfin/zdap/internal/config"
	"github.com/modfin/zdap/internal/core"
	"github.com/modfin/zdap/internal/utils"
	"github.com/modfin/zdap/internal/zfs"
	"net/http"
	"strconv"
	"time"
)
import "github.com/labstack/echo/v4"

func Start(cfg *config.Config, app *core.Core, z *zfs.ZFS) error {
	e := echo.New()

	e.Use(middleware.Logger())
	e.Use(middleware.RemoveTrailingSlash())

	e.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			auth := c.Request().Header.Get("auth")
			if len(auth) == 0 {
				fmt.Println(c.Request().Header)
				return errors.New("auth header must be supplied")
			}
			c.Set("owner", auth)
			return next(c)
		}
	})

	e.GET("/status", func(c echo.Context) error {
		dss, err := z.Open()
		if err != nil {
			return fmt.Errorf("could not open dataset, %w", err)
		}
		defer dss.Close()

		res, err := getStatus(dss, app)
		if err != nil {
			return fmt.Errorf("could not retrive status, %w", err)
		}
		return c.JSON(http.StatusOK, res)
	})

	e.GET("/resources", func(c echo.Context) error {
		dss, err := z.Open()
		if err != nil {
			return err
		}
		defer dss.Close()

		res, err := getResources(dss, c.Get("owner").(string), app)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, res)
	})

	e.GET("/resources/:resource", func(c echo.Context) error {
		dss, err := z.Open()
		if err != nil {
			return err
		}
		defer dss.Close()

		res, err := getResource(dss, c.Get("owner").(string), c.Param("resource"), app)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, res)
	})

	e.GET("/resources/:resource/clones", func(c echo.Context) error {
		dss, err := z.Open()
		if err != nil {
			return err
		}
		defer dss.Close()

		snaps, err := getSnaps(dss, c.Get("owner").(string), c.Param("resource"), app)
		if err != nil {
			return err
		}

		var clones []zdap.PublicClone

		for _, snap := range snaps {
			clones = append(clones, snap.Clones...)
		}

		return c.JSON(http.StatusOK, clones)
	})

	e.DELETE("/resources/:resource/clones", func(c echo.Context) error {
		dss, err := z.Open()
		if err != nil {
			return err
		}
		defer dss.Close()

		snaps, err := getSnaps(dss, c.Get("owner").(string), c.Param("resource"), app)
		if err != nil {
			return err
		}
		for _, snap := range snaps {
			for _, clone := range snap.Clones {
				err = app.DestroyClone(dss, clone.Name)
				if err != nil {
					return err
				}
			}
		}
		return c.NoContent(http.StatusOK)
	})

	e.DELETE("/resources/:resource/clones/:time", func(c echo.Context) error {
		dss, err := z.Open()
		fmt.Println("opened")
		if err != nil {
			fmt.Println(err.Error())
			return err
		}
		defer dss.Close()

		snaps, err := getSnaps(dss, c.Get("owner").(string), c.Param("resource"), app)
		fmt.Println("fetched snaps")
		if err != nil {
			fmt.Println(err.Error())
			return err
		}
		at, err := time.Parse(utils.TimestampFormat, c.Param("time"))
		fmt.Printf("parsed time%s", at)
		if err != nil {
			fmt.Println(err.Error())
			return err
		}

		for _, snap := range snaps {
			for _, clone := range snap.Clones {
				fmt.Println(clone.Name)
				if clone.CreatedAt.Equal(at) {
					fmt.Println("equals createdAt")
					err = app.DestroyClone(dss, clone.Name)
					return c.NoContent(http.StatusOK)
				}
			}
		}
		fmt.Println("nothing found")
		return errors.New("could not find clone to destroy")
	})

	e.GET("/resources/:resource/snaps", func(c echo.Context) error {
		dss, err := z.Open()
		if err != nil {
			return err
		}
		defer dss.Close()

		res, err := getSnaps(dss, c.Get("owner").(string), c.Param("resource"), app)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, res)
	})

	e.POST("/resources/:resource/snaps", func(c echo.Context) error {
		resource := c.Param("resource")

		dss, err := z.Open()
		if err != nil {
			return err
		}
		defer dss.Close()

		snaps, err := getSnaps(dss, c.Get("owner").(string), resource, app)
		if err != nil {
			return err
		}
		var max time.Time
		for _, s := range snaps {
			if s.CreatedAt.After(max) {
				max = s.CreatedAt
			}
		}
		clone, err := app.CloneResource(dss, c.Get("owner").(string), resource, max)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, clone)
	})

	e.POST("/resources/:resource/snaps/:createdAt", func(c echo.Context) error {
		resource := c.Param("resource")
		at, err := time.Parse(utils.TimestampFormat, c.Param("createdAt"))

		dss, err := z.Open()
		if err != nil {
			return err
		}
		defer dss.Close()

		clone, err := app.CloneResource(dss, c.Get("owner").(string), resource, at)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, clone)
	})

	e.GET("/resources/:resource/snaps/:createdAt", func(c echo.Context) error {
		at, err := time.Parse(utils.TimestampFormat, c.Param("createdAt"))
		if err != nil {
			return err
		}

		dss, err := z.Open()
		if err != nil {
			return err
		}
		defer dss.Close()

		res, err := getSnap(dss, c.Get("owner").(string), at, c.Param("resource"), app)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, res)
	})

	e.POST("/resources/:resource/claim", func(c echo.Context) error {
		resource := c.Param("resource")
		timeoutStr := c.QueryParam("ttl")
		timeout := internal.DefaultClaimTimeoutSeconds * time.Second
		if timeoutStr != "" {
			t, err := strconv.ParseInt(timeoutStr, 10, 64)
			if err == nil {
				timeout = time.Duration(t) * time.Second
			}
		}

		clone, err := app.ClaimPooledClone(resource, timeout)
		if err != nil {
			fmt.Println(err.Error())
			return c.JSON(http.StatusInternalServerError, err)
		}
		return c.JSON(http.StatusOK, clone)
	})

	fmt.Println("== Loaded Resources ==")
	for _, r := range app.GetResourcesNames() {
		fmt.Println(" -", r)
	}
	fmt.Println("== Starting Cron ==")
	fmt.Println(app.Start())

	fmt.Println("== Starting API Server ==")
	return e.Start(fmt.Sprintf(":%d", cfg.APIPort))
}
