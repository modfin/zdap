package api

import (
	"fmt"
	"github.com/docker/docker/client"
	"net/http"
	"time"
	"zdap/internal/config"
	"zdap/internal/core"
	"zdap/internal/utils"
	"zdap/internal/zfs"
)
import "github.com/labstack/echo/v4"

func Start(cfg *config.Config, app *core.Core, docker *client.Client, z *zfs.ZFS) error {
	e := echo.New()

	e.GET("/resources", func(c echo.Context) error {
		res, err := getResources(app)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, res)
	})

	e.GET("/resources/:resource", func(c echo.Context) error {
		res, err := getResource(c.Param("resource"), app)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, res)
	})

	e.POST("/resources/:resource", func(c echo.Context) error {
		resource := c.Param("resource")
		if !app.ResourcesExists(resource) {
			return fmt.Errorf("resource, %s, does not exist", resource)
		}

		go func() {
			err := app.CreateBaseAndSnap(resource)
			if err != nil {
				fmt.Println("could not create base and snap of", resource, ",", err)
			}
		}()
		return c.JSON(http.StatusOK, map[string]string{"status": "resource is being queued for creation of snap"})
	})
	e.GET("/resources/:resource/snaps", func(c echo.Context) error {
		res, err := getSnaps(c.Param("resource"), app)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, res)
	})

	e.POST("/resources/:resource/snaps", func(c echo.Context) error {
		resource := c.Param("resource")
		snaps, err := getSnaps(resource, app)
		if err != nil {
			return err
		}
		var max time.Time
		for _, s := range snaps {
			if s.CreatedAt.After(max) {
				max = s.CreatedAt
			}
		}

		clone, err := app.CloneResource(resource, max)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, clone)
	})
	e.POST("/resources/:resource/snaps/:createdAt", func(c echo.Context) error {
		resource := c.Param("resource")
		at, err := time.Parse(utils.TimestampFormat, c.Param("createdAt"))

		clone, err := app.CloneResource(resource, at)
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
		res, err := getSnap(at, c.Param("resource"), app)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, res)
	})
	e.GET("/resources/:resource/snaps/:createdAt/clones", func(c echo.Context) error {
		at, err := time.Parse(utils.TimestampFormat, c.Param("createdAt"))
		if err != nil {
			return err
		}
		res, err := getClones(at, c.Param("resource"), app)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, res)
	})
	e.GET("/resources/:resource/snaps/:snappedAt/clones/:clonedAt", func(c echo.Context) error {
		snapAt, err := time.Parse(utils.TimestampFormat, c.Param("snappedAt"))
		if err != nil {
			return err
		}
		clonedAt, err := time.Parse(utils.TimestampFormat, c.Param("clonedAt"))
		if err != nil {
			return err
		}
		res, err := getClone(clonedAt, snapAt, c.Param("resource"), app)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, res)
	})

	return e.Start(fmt.Sprintf(":%d", cfg.APIPort))
}
